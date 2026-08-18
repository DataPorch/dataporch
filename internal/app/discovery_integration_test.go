//go:build integration

package app

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/connection/mysql"
	"github.com/adamraziv/dataporch/internal/connection/postgres"
	"github.com/adamraziv/dataporch/internal/execution"
	"github.com/adamraziv/dataporch/internal/mcptoken"
	mcpTokenLocal "github.com/adamraziv/dataporch/internal/mcptoken/local"
	"github.com/jackc/pgx/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type integrationHarness struct {
	t                *testing.T
	dsn              string
	readerDSN        string
	names            integrationDatabaseNames
	admin            *pgx.Conn
	session          *mcpsdk.ClientSession
	runtime          *countingPostgresRuntime
	logs             *strings.Builder
	foreignAvailable bool
	serverVersion    int
}

type integrationSnapshot struct {
	Sources     execution.ListDataSourcesResult
	Schemas     []execution.Schema
	Tables      []execution.Table
	Columns     []execution.Column
	Constraints []execution.Constraint
}

func TestDiscoveryImportToMCPPostgresIntegration(t *testing.T) {
	t.Parallel()

	harness := newIntegrationHarness(t)
	sources := harness.assertDataSources()

	allSchemas := harness.listSchemas()
	harness.assertControlIdentifierTraversal(allSchemas)

	allTables := harness.listTables()
	allColumns, allConstraints := harness.listColumns()

	harness.assertLiteralSearch()

	harness.assertCompositeColumns()

	harness.assertColumnGrant()

	harness.assertDiscoveryFailures()

	harness.assertNoSecrets(integrationSnapshot{
		Sources:     sources,
		Schemas:     allSchemas,
		Tables:      allTables,
		Columns:     allColumns,
		Constraints: allConstraints,
	})
}

func newIntegrationHarness(t *testing.T) *integrationHarness {
	t.Helper()

	dsn := os.Getenv("DATAPORCH_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DATAPORCH_TEST_POSTGRES_DSN is not set")
	}

	names := integrationNames(t)
	admin := connectIntegrationAdmin(t, dsn)
	t.Cleanup(func() { cleanupIntegrationDatabase(t, admin, names) })
	serverVersion := integrationServerVersion(t, admin)
	foreignAvailable := setupIntegrationDatabase(t, admin, names, serverVersion)

	cfg := testConfigFor(t)
	cfg.ResourceLimit = 3

	cfg.HTTPAddress = freeTCPAddress(t)
	if err := InitializeSecrets(cfg); err != nil {
		t.Fatalf("InitializeSecrets() error = %v", err)
	}

	tokenStore, err := mcpTokenLocal.New(cfg.MCPTokenStorePath)
	if err != nil {
		t.Fatalf("mcpTokenLocal.New() error = %v", err)
	}

	tokenService, err := mcptoken.New(tokenStore, time.Now)
	if err != nil {
		t.Fatalf("mcptoken.New() error = %v", err)
	}

	mcpToken, _, err := tokenService.Create(t.Context())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	logs := &strings.Builder{}

	var runtime *countingPostgresRuntime

	application, err := newWithDependencies(
		cfg,
		slog.New(slog.NewTextHandler(logs, nil)),
		appDependencies{
			relationalModuleFactories: []relationalModuleFactory{
				newCountingPostgresModule(&runtime),
			},
			newExecutionService: execution.New,
		},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	startIntegrationApplication(
		t,
		application,
		cfg.AdminSocketPath,
		cfg.HTTPAddress,
	)
	readerDSN := importIntegrationSource(
		t,
		cfg.AdminSocketPath,
		dsn,
		names,
	)
	session := connectIntegrationMCP(t, cfg.HTTPAddress, mcpToken)

	return &integrationHarness{
		t:                t,
		dsn:              dsn,
		readerDSN:        readerDSN,
		names:            names,
		admin:            admin,
		session:          session,
		runtime:          runtime,
		logs:             logs,
		foreignAvailable: foreignAvailable,
		serverVersion:    serverVersion,
	}
}

func integrationServerVersion(t *testing.T, admin *pgx.Conn) int {
	t.Helper()

	var version int
	if err := admin.QueryRow(
		t.Context(),
		"SELECT current_setting('server_version_num')::integer",
	).Scan(&version); err != nil {
		t.Fatalf("querying PostgreSQL server version: %v", err)
	}

	return version
}

func connectIntegrationAdmin(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()

	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}

	t.Cleanup(func() {
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("admin.Close() error = %v", err)
		}
	})

	return admin
}

func startIntegrationApplication(
	t *testing.T,
	application *App,
	adminSocketPath string,
	httpAddress string,
) {
	t.Helper()

	appContext, cancel := context.WithCancel(t.Context())

	serverDone := make(chan error, 1)
	go func() { serverDone <- application.Run(appContext) }()

	t.Cleanup(func() {
		cancel()

		if err := <-serverDone; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})

	waitForFile(t, adminSocketPath)
	waitForHealth(t, httpAddress)
}

func importIntegrationSource(
	t *testing.T,
	adminSocketPath string,
	dsn string,
	names integrationDatabaseNames,
) string {
	t.Helper()

	readerDSN := readerConnectionString(t, dsn, names.role, names.password)

	reader, err := pgx.Connect(t.Context(), readerDSN)
	if err != nil {
		t.Fatalf("reader pgx.Connect() error = %v", err)
	}

	if err := reader.Close(t.Context()); err != nil {
		t.Fatalf("reader.Close() error = %v", err)
	}

	response, err := importOverSocket(
		adminSocketPath,
		string(names.sourceID),
		"postgres",
		readerDSN,
	)
	if err != nil {
		t.Fatalf("importOverSocket() error = %v", err)
	}

	if response.Body != nil {
		if err := response.Body.Close(); err != nil {
			t.Errorf("response.Body.Close() error = %v", err)
		}
	}

	if response.StatusCode != http.StatusCreated {
		t.Fatalf("import status = %d, want %d", response.StatusCode, http.StatusCreated)
	}

	return readerDSN
}

func connectIntegrationMCP(t *testing.T, address, token string) *mcpsdk.ClientSession {
	t.Helper()

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{
			Name:    "dataporch-integration",
			Version: "test",
		},
		nil,
	)

	session, err := client.Connect(
		t.Context(),
		&mcpsdk.StreamableClientTransport{
			Endpoint: "http://" + address + "/mcp",
			HTTPClient: &http.Client{
				Timeout:   30 * time.Second,
				Transport: integrationBearerTransport{token: token},
			},
			DisableStandaloneSSE: true,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("MCP Connect() error = %v", err)
	}

	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("session.Close() error = %v", err)
		}
	})

	return session
}

func newMySQLAppIntegrationSession(
	t *testing.T,
) (*mcpsdk.ClientSession, connection.ID, string) {
	t.Helper()

	dsn := os.Getenv("DATAPORCH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("DATAPORCH_TEST_MYSQL_DSN is not set")
	}

	parsed, err := mysql.New().ParseConnectionString([]byte(dsn))
	if err != nil {
		t.Fatalf("mysql ParseConnectionString() error = %v", err)
	}

	database := parsed.Settings["database"]
	for _, value := range parsed.Secrets {
		clear(value)
	}

	cfg := testConfigFor(t)

	cfg.HTTPAddress = freeTCPAddress(t)
	if err := InitializeSecrets(cfg); err != nil {
		t.Fatalf("InitializeSecrets() error = %v", err)
	}

	tokenStore, err := mcpTokenLocal.New(cfg.MCPTokenStorePath)
	if err != nil {
		t.Fatalf("mcpTokenLocal.New() error = %v", err)
	}

	tokenService, err := mcptoken.New(tokenStore, time.Now)
	if err != nil {
		t.Fatalf("mcptoken.New() error = %v", err)
	}

	token, _, err := tokenService.Create(t.Context())
	if err != nil {
		t.Fatalf("token Create() error = %v", err)
	}

	application, err := New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	startIntegrationApplication(t, application, cfg.AdminSocketPath, cfg.HTTPAddress)

	sourceID := connection.ID("mysql_app")

	response, err := importOverSocket(
		cfg.AdminSocketPath, string(sourceID), string(mysql.Kind), dsn,
	)
	if err != nil {
		t.Fatalf("importOverSocket() error = %v", err)
	}

	if response.Body != nil {
		_ = response.Body.Close()
	}

	if response.StatusCode != http.StatusCreated {
		t.Fatalf("import status = %d, want %d", response.StatusCode, http.StatusCreated)
	}

	return connectIntegrationMCP(t, cfg.HTTPAddress, token), sourceID, database
}

func TestDiscoveryImportToMCPMySQLIntegration(t *testing.T) {
	t.Parallel()

	session, sourceID, database := newMySQLAppIntegrationSession(t)

	page := callDiscoveryTool[execution.ListRelationalSchemasResult](
		t,
		session,
		"relational_database.list_schemas",
		map[string]any{"source_id": sourceID},
	)
	if len(page.Schemas) != 1 || page.Schemas[0].Name != database {
		t.Fatalf("schemas = %#v, want only %q", page.Schemas, database)
	}
}

type integrationBearerTransport struct {
	token string
}

func (t integrationBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.token)

	return http.DefaultTransport.RoundTrip(cloned)
}

func (h *integrationHarness) assertDataSources() execution.ListDataSourcesResult {
	h.t.Helper()

	sources := callDiscoveryTool[execution.ListDataSourcesResult](
		h.t,
		h.session,
		"data_source.list",
		nil,
	)
	if len(sources.Sources) != 1 || sources.Sources[0].ID != h.names.sourceID {
		h.t.Fatalf(
			"data sources = %#v, want imported source %q",
			sources.Sources,
			h.names.sourceID,
		)
	}

	capabilities := sources.Sources[0].Capabilities
	if len(capabilities) != 1 || capabilities[0] != execution.CapabilityRelationalDatabase {
		h.t.Fatalf("source capabilities = %#v, want relational_database", capabilities)
	}

	if got := h.runtime.openCount(); got != 0 {
		h.t.Fatalf("opens after data_source.list = %d, want zero", got)
	}

	return sources
}

func (h *integrationHarness) listSchemas() []execution.Schema {
	h.t.Helper()

	request := map[string]any{
		"source_id": h.names.sourceID,
		"search":    h.names.suffix,
		"limit":     2,
	}
	page := callDiscoveryTool[execution.ListRelationalSchemasResult](
		h.t,
		h.session,
		"relational_database.list_schemas",
		request,
	)

	schemas := append([]execution.Schema(nil), page.Schemas...)
	if page.NextCursor != "" {
		request["cursor"] = page.NextCursor
		nextPage := callDiscoveryTool[execution.ListRelationalSchemasResult](
			h.t,
			h.session,
			"relational_database.list_schemas",
			request,
		)
		schemas = append(schemas, nextPage.Schemas...)
	}

	if len(schemas) != 4 {
		h.t.Fatalf("schemas = %#v, want four accessible schemas", schemas)
	}

	assertSchemaNames(
		h.t,
		schemas,
		h.names.accessibleSchema,
		h.names.controlSchema,
		h.names.mixedSchema,
		h.names.secondarySchema,
	)

	if h.runtime.openCount() == 0 {
		h.t.Fatal("relational schema discovery did not open the database")
	}

	for _, schema := range schemas {
		if schema.Description != nil {
			h.t.Fatalf("schema description returned without flag: %#v", schema)
		}
	}

	described := callDiscoveryTool[execution.ListRelationalSchemasResult](
		h.t,
		h.session,
		"relational_database.list_schemas",
		map[string]any{
			"source_id":            h.names.sourceID,
			"search":               h.names.accessibleSchema,
			"include_descriptions": true,
		},
	)
	if len(described.Schemas) != 1 || described.Schemas[0].Description == nil {
		h.t.Fatalf(
			"described schemas = %#v, want requested schema description",
			described.Schemas,
		)
	}

	return schemas
}

func (h *integrationHarness) assertControlIdentifierTraversal(schemas []execution.Schema) {
	h.t.Helper()

	schema := schemaByName(h.t, schemas, h.names.controlSchema)

	tables := callDiscoveryTool[execution.ListRelationalTablesResult](
		h.t,
		h.session,
		"relational_database.list_tables",
		map[string]any{
			"source_id": h.names.sourceID,
			"schema":    schema.Name,
		},
	)
	if len(tables.Tables) != 1 || tables.Tables[0].Name != h.names.controlTable {
		h.t.Fatalf(
			"control identifier tables = %#v, want %q",
			tables.Tables,
			h.names.controlTable,
		)
	}

	columns := callDiscoveryTool[execution.ListRelationalColumnsResult](
		h.t,
		h.session,
		"relational_database.list_columns",
		map[string]any{
			"source_id": h.names.sourceID,
			"schema":    schema.Name,
			"table":     tables.Tables[0].Name,
		},
	)
	if len(columns.Columns) != 1 || columns.Columns[0].Name != "id" {
		h.t.Fatalf("control identifier columns = %#v, want id", columns.Columns)
	}
}

func (h *integrationHarness) listTables() []execution.Table {
	h.t.Helper()

	request := map[string]any{
		"source_id": h.names.sourceID,
		"schema":    h.names.accessibleSchema,
		"limit":     3,
	}
	page := callDiscoveryTool[execution.ListRelationalTablesResult](
		h.t,
		h.session,
		"relational_database.list_tables",
		request,
	)

	tables := append([]execution.Table(nil), page.Tables...)
	for page.NextCursor != "" {
		request["cursor"] = page.NextCursor
		page = callDiscoveryTool[execution.ListRelationalTablesResult](
			h.t,
			h.session,
			"relational_database.list_tables",
			request,
		)
		tables = append(tables, page.Tables...)
	}

	assertTableKind(h.t, tables, h.names.ordinaryTable, execution.RelationKindTable)
	assertTableKind(
		h.t,
		tables,
		h.names.partitionedTable,
		execution.RelationKindPartitionedTable,
	)
	assertTableKind(h.t, tables, h.names.view, execution.RelationKindView)
	assertTableKind(
		h.t,
		tables,
		h.names.materializedView,
		execution.RelationKindMaterializedView,
	)

	if hasTable(tables, h.names.deniedTable) {
		h.t.Fatalf("tables include unreadable relation %q: %#v", h.names.deniedTable, tables)
	}

	if h.foreignAvailable {
		assertTableKind(
			h.t,
			tables,
			h.names.foreignTable,
			execution.RelationKindForeignTable,
		)
	}

	for _, table := range tables {
		if table.Description != nil {
			h.t.Fatalf("table description returned without flag: %#v", table)
		}
	}

	h.assertDescribedTable()
	h.assertMixedCaseTable()

	return tables
}

func (h *integrationHarness) assertDescribedTable() {
	h.t.Helper()

	described := callDiscoveryTool[execution.ListRelationalTablesResult](
		h.t,
		h.session,
		"relational_database.list_tables",
		map[string]any{
			"source_id":            h.names.sourceID,
			"schema":               h.names.accessibleSchema,
			"search":               h.names.ordinaryTable,
			"include_descriptions": true,
		},
	)
	if len(described.Tables) != 1 || described.Tables[0].Description == nil {
		h.t.Fatalf(
			"described tables = %#v, want requested table description",
			described.Tables,
		)
	}
}

func (h *integrationHarness) assertMixedCaseTable() {
	h.t.Helper()

	tables := callDiscoveryTool[execution.ListRelationalTablesResult](
		h.t,
		h.session,
		"relational_database.list_tables",
		map[string]any{
			"source_id": h.names.sourceID,
			"schema":    h.names.mixedSchema,
		},
	)
	if !hasTable(tables.Tables, h.names.mixedTable) {
		h.t.Fatalf("mixed-case tables = %#v, want %q", tables.Tables, h.names.mixedTable)
	}
}

func (h *integrationHarness) listColumns() ([]execution.Column, []execution.Constraint) {
	h.t.Helper()

	request := map[string]any{
		"source_id": h.names.sourceID,
		"schema":    h.names.accessibleSchema,
		"table":     h.names.ordinaryTable,
		"limit":     3,
	}
	page := callDiscoveryTool[execution.ListRelationalColumnsResult](
		h.t,
		h.session,
		"relational_database.list_columns",
		request,
	)
	columns := append([]execution.Column(nil), page.Columns...)

	constraints := append([]execution.Constraint(nil), page.Constraints...)
	for page.NextCursor != "" {
		request["cursor"] = page.NextCursor
		page = callDiscoveryTool[execution.ListRelationalColumnsResult](
			h.t,
			h.session,
			"relational_database.list_columns",
			request,
		)
		columns = append(columns, page.Columns...)
		constraints = append(constraints, page.Constraints...)
	}

	if len(columns) < 8 {
		h.t.Fatalf("columns = %#v, want metadata-rich ordinary table", columns)
	}

	if len(constraints) < 3 {
		h.t.Fatalf("constraints = %#v, want primary, foreign, and check constraints", constraints)
	}

	assertColumnMetadata(h.t, columns, h.expectedGeneratedKind())
	h.assertOrdinaryConstraints(constraints)

	for _, column := range columns {
		if column.Description != nil {
			h.t.Fatalf("column description returned without flag: %#v", column)
		}
	}

	h.assertDescribedColumn()

	return columns, constraints
}

func (h *integrationHarness) expectedGeneratedKind() string {
	if h.serverVersion >= 180000 {
		return "virtual"
	}

	return "stored"
}

func (h *integrationHarness) assertOrdinaryConstraints(constraints []execution.Constraint) {
	h.t.Helper()

	primaryName := "orders_" + h.names.suffix + "_pkey"
	foreignName := "orders_" + h.names.suffix + "_customer_id_fkey"
	primaryIsPresent := hasConstraint(constraints, primaryName, "primary_key")

	foreignIsPresent := hasConstraint(constraints, foreignName, "foreign_key")
	if !primaryIsPresent || !foreignIsPresent {
		h.t.Fatalf("constraints omit expected keys: %#v", constraints)
	}

	checkName := "orders_" + h.names.suffix + "_amount_check"
	if !hasConstraint(constraints, checkName, "check") {
		h.t.Fatalf("constraints omit check constraint: %#v", constraints)
	}
}

func (h *integrationHarness) assertDescribedColumn() {
	h.t.Helper()

	described := callDiscoveryTool[execution.ListRelationalColumnsResult](
		h.t,
		h.session,
		"relational_database.list_columns",
		map[string]any{
			"source_id":            h.names.sourceID,
			"schema":               h.names.accessibleSchema,
			"table":                h.names.ordinaryTable,
			"include_descriptions": true,
		},
	)
	if !hasDescribedColumn(described.Columns, "amount") {
		h.t.Fatalf("described columns = %#v, want amount description", described.Columns)
	}
}

func (h *integrationHarness) assertLiteralSearch() {
	h.t.Helper()

	result := callDiscoveryTool[execution.ListRelationalTablesResult](
		h.t,
		h.session,
		"relational_database.list_tables",
		map[string]any{
			"source_id": h.names.sourceID,
			"schema":    h.names.accessibleSchema,
			"search":    `%_*`,
		},
	)
	if len(result.Tables) != 0 {
		h.t.Fatalf("literal search tables = %#v, want no wildcard interpretation", result.Tables)
	}
}

func (h *integrationHarness) assertCompositeColumns() {
	h.t.Helper()

	request := map[string]any{
		"source_id": h.names.sourceID,
		"schema":    h.names.accessibleSchema,
		"table":     h.names.compositeChild,
		"limit":     3,
	}
	page := callDiscoveryTool[execution.ListRelationalColumnsResult](
		h.t,
		h.session,
		"relational_database.list_columns",
		request,
	)

	constraints := append([]execution.Constraint(nil), page.Constraints...)
	for page.NextCursor != "" {
		request["cursor"] = page.NextCursor
		page = callDiscoveryTool[execution.ListRelationalColumnsResult](
			h.t,
			h.session,
			"relational_database.list_columns",
			request,
		)
		constraints = append(constraints, page.Constraints...)
	}

	assertCompositeConstraints(h.t, constraints, h.names)
}

func (h *integrationHarness) assertColumnGrant() {
	h.t.Helper()

	result := callDiscoveryTool[execution.ListRelationalColumnsResult](
		h.t,
		h.session,
		"relational_database.list_columns",
		map[string]any{
			"source_id": h.names.sourceID,
			"schema":    h.names.accessibleSchema,
			"table":     h.names.columnGrantTable,
		},
	)
	if len(result.Columns) != 1 || result.Columns[0].Name != "visible_value" {
		h.t.Fatalf("column-grant columns = %#v, want only visible_value", result.Columns)
	}
}

func (h *integrationHarness) assertDiscoveryFailures() {
	h.t.Helper()

	tests := []struct {
		name      string
		tool      string
		arguments map[string]any
		expected  execution.ErrorCategory
	}{
		{
			name: "missing schema",
			tool: "relational_database.list_tables",
			arguments: map[string]any{
				"source_id": h.names.sourceID,
				"schema":    "missing_" + h.names.suffix,
			},
			expected: execution.ErrorCategorySchemaNotFound,
		},
		{
			name: "denied schema",
			tool: "relational_database.list_tables",
			arguments: map[string]any{
				"source_id": h.names.sourceID,
				"schema":    h.names.deniedSchema,
			},
			expected: execution.ErrorCategoryDatabasePermissionDenied,
		},
		{
			name: "missing relation",
			tool: "relational_database.list_columns",
			arguments: map[string]any{
				"source_id": h.names.sourceID,
				"schema":    h.names.accessibleSchema,
				"table":     "missing_" + h.names.suffix,
			},
			expected: execution.ErrorCategoryRelationNotFound,
		},
		{
			name: "denied relation",
			tool: "relational_database.list_columns",
			arguments: map[string]any{
				"source_id": h.names.sourceID,
				"schema":    h.names.accessibleSchema,
				"table":     h.names.deniedTable,
			},
			expected: execution.ErrorCategoryDatabasePermissionDenied,
		},
	}
	for _, test := range tests {
		h.t.Run(test.name, func(t *testing.T) {
			failure := callDiscoveryToolFailure(
				t,
				h.session,
				test.tool,
				test.arguments,
			)
			if failure.Category != test.expected {
				t.Fatalf("failure = %#v, want category %q", failure, test.expected)
			}
		})
	}
}

func (h *integrationHarness) assertNoSecrets(snapshot integrationSnapshot) {
	h.t.Helper()

	observed, err := json.Marshal(snapshot)
	if err != nil {
		h.t.Fatalf("Marshal(observed) error = %v", err)
	}

	canaries := []string{
		h.dsn,
		h.readerDSN,
		h.names.password,
		h.names.role,
	}
	assertIntegrationSecretsAbsent(h.t, observed, canaries...)
	assertIntegrationSecretsAbsent(h.t, []byte(h.logs.String()), canaries...)

	if h.runtime.openCount() == 0 {
		h.t.Fatal("relational discovery did not increment opener count")
	}
}

func TestContainsSensitiveValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     string
		expected bool
	}{
		{
			name:     "application name is allowed",
			data:     "dataporch listening",
			expected: false,
		},
		{
			name:     "password is rejected",
			data:     "password=secret_canary_4f9d",
			expected: true,
		},
		{
			name:     "role is rejected",
			data:     "role=reader_canary_4f9d",
			expected: true,
		},
		{
			name:     "dsn is rejected",
			data:     "postgres://dataporch:secret_canary_4f9d@localhost/dataporch",
			expected: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			values := []string{
				"secret_canary_4f9d",
				"reader_canary_4f9d",
				"postgres://dataporch:secret_canary_4f9d@localhost/dataporch",
			}
			if got := containsSensitiveValue([]byte(test.data), values...); got != test.expected {
				t.Fatalf("containsSensitiveValue() = %t, want %t", got, test.expected)
			}
		})
	}
}

type integrationDatabaseNames struct {
	suffix           string
	sourceID         connection.ID
	role             string
	password         string
	accessibleSchema string
	secondarySchema  string
	mixedSchema      string
	controlSchema    string
	deniedSchema     string
	ordinaryTable    string
	partitionedTable string
	view             string
	materializedView string
	deniedTable      string
	columnGrantTable string
	mixedTable       string
	controlTable     string
	foreignTable     string
	server           string
	compositeParent  string
	compositeChild   string
}

func integrationNames(t *testing.T) integrationDatabaseNames {
	t.Helper()

	random := make([]byte, 6)
	if _, err := cryptorand.Read(random); err != nil {
		t.Fatalf("crypto/rand.Read() error = %v", err)
	}

	prefix := strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))

	prefix = strings.Trim(prefix, "_")
	if len(prefix) > 20 {
		prefix = prefix[:20]
	}

	suffix := prefix + "_" + hex.EncodeToString(random)

	return integrationDatabaseNames{
		suffix:           suffix,
		sourceID:         connection.ID("integration_" + hex.EncodeToString(random)),
		role:             "dp_reader_" + suffix,
		password:         "dp_canary_" + suffix,
		accessibleSchema: "dp_access_" + suffix,
		secondarySchema:  "dp_second_" + suffix,
		mixedSchema:      "DpMixed_" + suffix,
		controlSchema:    "dp_control_" + suffix + "\narchive",
		deniedSchema:     "dp_denied_" + suffix,
		ordinaryTable:    "orders_" + suffix,
		partitionedTable: "events_" + suffix,
		view:             "orders_view_" + suffix,
		materializedView: "orders_mv_" + suffix,
		deniedTable:      "hidden_" + suffix,
		columnGrantTable: "column_grant_" + suffix,
		mixedTable:       "MixedOrders_" + suffix,
		controlTable:     "Control\tOrders_" + suffix,
		foreignTable:     "foreign_" + suffix,
		server:           "dp_server_" + suffix,
		compositeParent:  "composite_parent_" + suffix,
		compositeChild:   "composite_child_" + suffix,
	}
}

func setupIntegrationDatabase(
	t *testing.T,
	admin *pgx.Conn,
	names integrationDatabaseNames,
	serverVersion int,
) bool {
	t.Helper()

	accessible, role := setupIntegrationSchemas(t, admin, names)

	relations, orders := setupIntegrationMetadataRelations(
		t,
		admin,
		names,
		accessible,
		serverVersion,
	)

	relations = append(
		relations,
		setupIntegrationPartitions(t, admin, names, accessible)...,
	)

	relations = append(
		relations,
		setupIntegrationTableKinds(t, admin, names, accessible, orders)...,
	)

	relations = append(relations, setupIntegrationControlRelation(t, admin, names))

	for _, relation := range relations {
		execIntegrationSQL(t, admin, fmt.Sprintf("GRANT SELECT ON %s TO %s", relation, role))
	}

	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf(
			"GRANT SELECT (visible_value) ON %s.%s TO %s",
			accessible,
			integrationIdentifier(names.columnGrantTable),
			role,
		),
	)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf(
			"GRANT USAGE ON TYPE %s.order_state, %s.customer_code TO %s",
			accessible,
			accessible,
			role,
		),
	)

	foreignAvailable := true
	if err := setupOptionalForeignTable(t, admin, names); err != nil {
		foreignAvailable = false

		t.Logf("skipping optional postgres_fdw relation: %v", err)
	}

	return foreignAvailable
}

func setupIntegrationSchemas(
	t *testing.T,
	admin *pgx.Conn,
	names integrationDatabaseNames,
) (string, string) {
	t.Helper()

	accessible := integrationIdentifier(names.accessibleSchema)
	role := integrationIdentifier(names.role)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf(
			"CREATE ROLE %s LOGIN PASSWORD %s",
			role,
			integrationLiteral(names.password),
		),
	)

	adminConfig, err := pgx.ParseConfig(os.Getenv("DATAPORCH_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatalf("pgx.ParseConfig() error = %v", err)
	}

	grantConnect := fmt.Sprintf(
		"GRANT CONNECT ON DATABASE %s TO %s",
		integrationIdentifier(adminConfig.Database),
		role,
	)
	execIntegrationSQL(t, admin, grantConnect)

	schemas := []string{
		names.accessibleSchema,
		names.secondarySchema,
		names.mixedSchema,
		names.controlSchema,
		names.deniedSchema,
	}
	for _, schema := range schemas {
		execIntegrationSQL(t, admin, "CREATE SCHEMA "+integrationIdentifier(schema))
	}

	grantSchemaUsage := fmt.Sprintf(
		"GRANT USAGE ON SCHEMA %s, %s, %s, %s TO %s",
		accessible,
		integrationIdentifier(names.secondarySchema),
		integrationIdentifier(names.mixedSchema),
		integrationIdentifier(names.controlSchema),
		role,
	)
	execIntegrationSQL(t, admin, grantSchemaUsage)

	return accessible, role
}

func setupIntegrationMetadataRelations(
	t *testing.T,
	admin *pgx.Conn,
	names integrationDatabaseNames,
	accessible string,
	serverVersion int,
) ([]string, string) {
	t.Helper()

	customers := accessible + "." + integrationIdentifier("customers_"+names.suffix)
	orders := accessible + "." + integrationIdentifier(names.ordinaryTable)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf("CREATE TYPE %s.order_state AS ENUM ('open', 'closed')", accessible),
	)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf(
			"CREATE DOMAIN %s.customer_code AS text NOT NULL CHECK (VALUE <> '')",
			accessible,
		),
	)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf("CREATE TABLE %s (id bigint PRIMARY KEY, name text UNIQUE)", customers),
	)

	generatedColumn := "generated_amount numeric GENERATED ALWAYS AS (amount * 2) STORED"
	if serverVersion >= 180000 {
		generatedColumn = "generated_amount numeric GENERATED ALWAYS AS (amount * 2)"
	}

	createOrders := fmt.Sprintf(
		"CREATE TABLE %s ("+
			"id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, "+
			"customer_id bigint REFERENCES %s(id), "+
			"amount numeric(12,2) NOT NULL DEFAULT 0 CHECK (amount >= 0), "+
			"code %s.customer_code, state %s.order_state DEFAULT 'open', "+
			"tags text[], created_at timestamp(3), %s)",
		orders,
		customers,
		accessible,
		accessible,
		generatedColumn,
	)
	execIntegrationSQL(t, admin, createOrders)

	compositeParent := accessible + "." + integrationIdentifier(names.compositeParent)
	compositeChild := accessible + "." + integrationIdentifier(names.compositeChild)
	createCompositeParent := fmt.Sprintf(
		"CREATE TABLE %s ("+
			"tenant_id bigint NOT NULL, parent_id bigint NOT NULL, code text NOT NULL, "+
			"CONSTRAINT %s PRIMARY KEY (tenant_id, parent_id), "+
			"CONSTRAINT %s UNIQUE (tenant_id, code))",
		compositeParent,
		integrationIdentifier(names.compositeParent+"_pkey"),
		integrationIdentifier(names.compositeParent+"_unique"),
	)
	execIntegrationSQL(t, admin, createCompositeParent)

	createCompositeChild := fmt.Sprintf(
		"CREATE TABLE %s ("+
			"tenant_id bigint NOT NULL, parent_id bigint NOT NULL, "+
			"code text NOT NULL, amount numeric NOT NULL, "+
			"CONSTRAINT %s PRIMARY KEY (tenant_id, parent_id), "+
			"CONSTRAINT %s UNIQUE (tenant_id, code), "+
			"CONSTRAINT %s FOREIGN KEY (tenant_id, parent_id) "+
			"REFERENCES %s (tenant_id, parent_id), "+
			"CONSTRAINT %s CHECK (amount >= 0))",
		compositeChild,
		integrationIdentifier(names.compositeChild+"_pkey"),
		integrationIdentifier(names.compositeChild+"_unique"),
		integrationIdentifier(names.compositeChild+"_parent_fkey"),
		compositeParent,
		integrationIdentifier(names.compositeChild+"_amount_check"),
	)
	execIntegrationSQL(t, admin, createCompositeChild)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf("COMMENT ON SCHEMA %s IS 'schema description'", accessible),
	)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf("COMMENT ON TABLE %s IS 'orders description'", orders),
	)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf("COMMENT ON COLUMN %s.amount IS 'amount description'", orders),
	)

	return []string{orders, customers, compositeParent, compositeChild}, orders
}

func setupIntegrationPartitions(
	t *testing.T,
	admin *pgx.Conn,
	names integrationDatabaseNames,
	accessible string,
) []string {
	t.Helper()

	partitioned := accessible + "." + integrationIdentifier(names.partitionedTable)
	partition := accessible + "." + integrationIdentifier(names.partitionedTable+"_2026")
	createPartitioned := fmt.Sprintf(
		"CREATE TABLE %s (id bigint, happened_at date, "+
			"PRIMARY KEY (id, happened_at)) PARTITION BY RANGE (happened_at)",
		partitioned,
	)
	execIntegrationSQL(t, admin, createPartitioned)

	createPartition := fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF %s "+
			"FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')",
		partition,
		partitioned,
	)
	execIntegrationSQL(t, admin, createPartition)

	return []string{partitioned, partition}
}

func setupIntegrationTableKinds(
	t *testing.T,
	admin *pgx.Conn,
	names integrationDatabaseNames,
	accessible string,
	orders string,
) []string {
	t.Helper()

	view := accessible + "." + integrationIdentifier(names.view)
	materialized := accessible + "." + integrationIdentifier(names.materializedView)
	mixed := integrationIdentifier(names.mixedSchema) + "." + integrationIdentifier(names.mixedTable)

	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf("CREATE VIEW %s AS SELECT id, amount FROM %s", view, orders),
	)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf("CREATE MATERIALIZED VIEW %s AS SELECT id, amount FROM %s", materialized, orders),
	)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf(
			"CREATE TABLE %s.%s (id bigint, secret_value text)",
			accessible,
			integrationIdentifier(names.deniedTable),
		),
	)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf(
			"CREATE TABLE %s.%s (visible_value text, hidden_value text)",
			accessible,
			integrationIdentifier(names.columnGrantTable),
		),
	)
	execIntegrationSQL(
		t,
		admin,
		fmt.Sprintf("CREATE TABLE %s (id bigint)", mixed),
	)

	return []string{view, materialized, mixed}
}

func setupIntegrationControlRelation(
	t *testing.T,
	admin *pgx.Conn,
	names integrationDatabaseNames,
) string {
	t.Helper()

	relation := integrationIdentifier(names.controlSchema) +
		"." + integrationIdentifier(names.controlTable)
	execIntegrationSQL(t, admin, "CREATE TABLE "+relation+" (id bigint)")

	return relation
}

func setupOptionalForeignTable(t *testing.T, admin *pgx.Conn, names integrationDatabaseNames) error {
	t.Helper()

	if _, err := admin.Exec(t.Context(), "CREATE EXTENSION IF NOT EXISTS postgres_fdw"); err != nil {
		return fmt.Errorf("creating postgres_fdw extension: %w", err)
	}

	parsed, err := pgx.ParseConfig(os.Getenv("DATAPORCH_TEST_POSTGRES_DSN"))
	if err != nil {
		return fmt.Errorf("parsing integration DSN: %w", err)
	}

	server := integrationIdentifier(names.server)
	foreign := integrationIdentifier(names.accessibleSchema) + "." + integrationIdentifier(names.foreignTable)
	port := integrationLiteral(strconv.FormatUint(uint64(parsed.Port), 10))

	statements := []string{
		fmt.Sprintf(
			"CREATE SERVER %s FOREIGN DATA WRAPPER postgres_fdw "+
				"OPTIONS (host %s, port %s, dbname %s)",
			server,
			integrationLiteral(parsed.Host),
			port,
			integrationLiteral(parsed.Database),
		),
		fmt.Sprintf(
			"CREATE USER MAPPING FOR %s SERVER %s "+
				"OPTIONS (user %s, password %s)",
			integrationIdentifier(names.role),
			server,
			integrationLiteral(parsed.User),
			integrationLiteral(parsed.Password),
		),
		fmt.Sprintf(
			"CREATE FOREIGN TABLE %s (id bigint) SERVER %s "+
				"OPTIONS (schema_name %s, table_name %s)",
			foreign,
			server,
			integrationLiteral(names.accessibleSchema),
			integrationLiteral("customers_"+names.suffix),
		),
		fmt.Sprintf(
			"GRANT USAGE ON FOREIGN SERVER %s TO %s",
			server,
			integrationIdentifier(names.role),
		),
		fmt.Sprintf("GRANT SELECT ON %s TO %s", foreign, integrationIdentifier(names.role)),
	}
	for _, statement := range statements {
		if _, err := admin.Exec(t.Context(), statement); err != nil {
			return err
		}
	}

	return nil
}

func cleanupIntegrationDatabase(t *testing.T, admin *pgx.Conn, names integrationDatabaseNames) {
	t.Helper()

	for _, statement := range []string{
		fmt.Sprintf("DROP SERVER IF EXISTS %s CASCADE", integrationIdentifier(names.server)),
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", integrationIdentifier(names.accessibleSchema)),
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", integrationIdentifier(names.secondarySchema)),
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", integrationIdentifier(names.mixedSchema)),
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", integrationIdentifier(names.controlSchema)),
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", integrationIdentifier(names.deniedSchema)),
		"DROP OWNED BY " + integrationIdentifier(names.role),
		"DROP ROLE IF EXISTS " + integrationIdentifier(names.role),
	} {
		if _, err := admin.Exec(context.Background(), statement); err != nil {
			t.Errorf("cleanup statement failed: %v", err)
		}
	}
}

func callDiscoveryTool[T any](
	t *testing.T,
	session *mcpsdk.ClientSession,
	name string,
	arguments map[string]any,
) T {
	t.Helper()

	if arguments == nil {
		arguments = map[string]any{}
	}

	result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("CallTool(%q) error = %v", name, err)
	}

	if result.IsError {
		if len(result.Content) == 1 {
			if text, ok := result.Content[0].(*mcpsdk.TextContent); ok {
				t.Fatalf("CallTool(%q) returned tool error: %s", name, text.Text)
			}
		}

		t.Fatalf("CallTool(%q) returned tool error: %#v", name, result.Content)
	}

	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("Marshal(%q structured content) error = %v", name, err)
	}

	var output T
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("Unmarshal(%q result) error = %v", name, err)
	}

	return output
}

func callDiscoveryToolFailure(
	t *testing.T,
	session *mcpsdk.ClientSession,
	name string,
	arguments map[string]any,
) execution.Failure {
	t.Helper()

	result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("CallTool(%q) error = %v", name, err)
	}

	if !result.IsError || len(result.Content) != 1 || result.StructuredContent != nil {
		t.Fatalf("CallTool(%q) result = %#v, want safe tool error", name, result)
	}

	text, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("CallTool(%q) content type = %T, want text", name, result.Content[0])
	}

	var failure execution.Failure
	if err := json.Unmarshal([]byte(text.Text), &failure); err != nil {
		t.Fatalf("CallTool(%q) error text = %q: %v", name, text.Text, err)
	}

	return failure
}

type countingPostgresRuntime struct {
	opener *postgres.Opener
	opens  atomic.Int64
}

func newCountingPostgresModule(
	runtime **countingPostgresRuntime,
) relationalModuleFactory {
	return func(
		manager *connection.Manager,
		policy queryPolicy,
	) (relationalModule, error) {
		opener, err := postgres.NewOpener(manager)
		if err != nil {
			return relationalModule{}, err
		}

		counting := &countingPostgresRuntime{opener: opener}

		discoverer, err := postgres.NewDiscoverer(counting)
		if err != nil {
			return relationalModule{}, errors.Join(err, counting.Close(context.Background()))
		}

		queryExecutor, err := postgres.NewQueryExecutor(counting, postgres.QueryOptions{
			Timeout:           policy.timeout,
			ResponseByteLimit: policy.responseByteLimit,
			TruncationEnabled: policy.truncationEnabled,
			RowLimit:          policy.rowLimit,
		})
		if err != nil {
			return relationalModule{}, errors.Join(err, counting.Close(context.Background()))
		}

		*runtime = counting

		return relationalModule{
			adapter:       postgres.New(),
			discoverer:    discoverer,
			queryExecutor: queryExecutor,
			runtime:       counting,
		}, nil
	}
}

func (r *countingPostgresRuntime) Open(ctx context.Context, id connection.ID) (*postgres.Client, error) {
	r.opens.Add(1)
	return r.opener.Open(ctx, id)
}

func (r *countingPostgresRuntime) OpenQuery(
	ctx context.Context,
	id connection.ID,
) (*postgres.Client, error) {
	r.opens.Add(1)
	return r.opener.OpenQuery(ctx, id)
}

func (r *countingPostgresRuntime) Invalidate(id connection.ID) {
	r.opener.Invalidate(id)
}

func (r *countingPostgresRuntime) Close(ctx context.Context) error {
	return r.opener.Close(ctx)
}

func (r *countingPostgresRuntime) openCount() int64 {
	return r.opens.Load()
}

func assertSchemaNames(t *testing.T, schemas []execution.Schema, expected ...string) {
	t.Helper()

	for _, name := range expected {
		found := false

		for _, schema := range schemas {
			if schema.Name == name {
				found = true
				break
			}
		}

		if !found {
			t.Fatalf("schemas = %#v, missing %q", schemas, name)
		}
	}
}

func schemaByName(t *testing.T, schemas []execution.Schema, name string) execution.Schema {
	t.Helper()

	for _, schema := range schemas {
		if schema.Name == name {
			return schema
		}
	}

	t.Fatalf("schemas = %#v, missing %q", schemas, name)

	return execution.Schema{}
}

func assertTableKind(t *testing.T, tables []execution.Table, name string, kind execution.RelationKind) {
	t.Helper()

	for _, table := range tables {
		if table.Name == name {
			if table.Kind != kind {
				t.Fatalf("table %q kind = %q, want %q", name, table.Kind, kind)
			}

			return
		}
	}

	t.Fatalf("tables = %#v, missing %q", tables, name)
}

func hasTable(tables []execution.Table, name string) bool {
	for _, table := range tables {
		if table.Name == name {
			return true
		}
	}

	return false
}

func assertColumnMetadata(t *testing.T, columns []execution.Column, expectedGeneratedKind string) {
	t.Helper()

	assertIdentityColumnMetadata(t, columnByName(t, columns, "id"))
	assertAmountColumnMetadata(t, columnByName(t, columns, "amount"))
	assertDomainColumnMetadata(t, columnByName(t, columns, "code"))
	assertEnumColumnMetadata(t, columnByName(t, columns, "state"))
	assertArrayColumnMetadata(t, columnByName(t, columns, "tags"))
	assertTemporalColumnMetadata(t, columnByName(t, columns, "created_at"))
	assertGeneratedColumnMetadata(
		t,
		columnByName(t, columns, "generated_amount"),
		expectedGeneratedKind,
	)
}

func assertIdentityColumnMetadata(t *testing.T, column execution.Column) {
	t.Helper()

	if column.Identity == nil || column.Identity.Generation != "always" {
		t.Fatalf("column %q metadata = %#v, want always identity", column.Name, column)
	}
}

func assertAmountColumnMetadata(t *testing.T, column execution.Column) {
	t.Helper()

	metadataIsMissing := column.DefaultExpression == nil ||
		column.Type.Precision == nil ||
		column.Type.Scale == nil
	if column.Nullable || metadataIsMissing {
		t.Fatalf("column %q metadata = %#v, want numeric metadata", column.Name, column)
	}

	if *column.Type.Precision != 12 || *column.Type.Scale != 2 {
		t.Fatalf("column %q metadata = %#v, want numeric(12,2)", column.Name, column)
	}
}

func assertDomainColumnMetadata(t *testing.T, column execution.Column) {
	t.Helper()

	domainBaseIsMissing := column.Type.DomainBaseType == nil
	if column.Type.Category != execution.TypeCategoryDomain || domainBaseIsMissing {
		t.Fatalf("column %q metadata = %#v, want domain metadata", column.Name, column)
	}

	if column.Type.DomainBaseType.Name != "text" {
		t.Fatalf("column %q metadata = %#v, want text domain base", column.Name, column)
	}

	if column.Nullable {
		t.Fatalf("column %q metadata = %#v, want NOT NULL domain", column.Name, column)
	}
}

func assertEnumColumnMetadata(t *testing.T, column execution.Column) {
	t.Helper()

	if column.Type.Category != execution.TypeCategoryEnum || column.DefaultExpression == nil {
		t.Fatalf("column %q metadata = %#v, want enum metadata", column.Name, column)
	}
}

func assertArrayColumnMetadata(t *testing.T, column execution.Column) {
	t.Helper()

	arrayMetadataIsMissing := column.Type.ElementType == nil
	if column.Type.Category != execution.TypeCategoryArray || !column.Type.IsArray || arrayMetadataIsMissing {
		t.Fatalf("column %q metadata = %#v, want array metadata", column.Name, column)
	}

	if column.Type.ElementType.Name != "text" {
		t.Fatalf("column %q metadata = %#v, want text array element", column.Name, column)
	}
}

func assertTemporalColumnMetadata(t *testing.T, column execution.Column) {
	t.Helper()

	precision := column.Type.TemporalPrecision
	if precision == nil || *precision != 3 {
		t.Fatalf("column %q metadata = %#v, want temporal precision", column.Name, column)
	}
}

func assertGeneratedColumnMetadata(
	t *testing.T,
	column execution.Column,
	expectedKind string,
) {
	t.Helper()

	if column.Generated == nil || column.Generated.Kind != expectedKind {
		t.Fatalf(
			"column %q metadata = %#v, want %s generation",
			column.Name,
			column,
			expectedKind,
		)
	}

	if !strings.Contains(column.Generated.Expression, "amount") {
		t.Fatalf("column %q metadata = %#v, want generated expression", column.Name, column)
	}
}

func columnByName(t *testing.T, columns []execution.Column, name string) execution.Column {
	t.Helper()

	for _, column := range columns {
		if column.Name == name {
			return column
		}
	}

	t.Fatalf("columns = %#v, missing %q", columns, name)

	return execution.Column{}
}

func assertCompositeConstraints(t *testing.T, constraints []execution.Constraint, names integrationDatabaseNames) {
	t.Helper()

	wants := map[string]struct {
		kind    string
		columns []string
	}{
		names.compositeChild + "_pkey":         {kind: "primary_key", columns: []string{"tenant_id", "parent_id"}},
		names.compositeChild + "_unique":       {kind: "unique", columns: []string{"tenant_id", "code"}},
		names.compositeChild + "_parent_fkey":  {kind: "foreign_key", columns: []string{"tenant_id", "parent_id"}},
		names.compositeChild + "_amount_check": {kind: "check", columns: []string{"amount"}},
	}

	for name, want := range wants {
		var found *execution.Constraint

		for index := range constraints {
			if constraints[index].Name == name {
				found = &constraints[index]
				break
			}
		}

		if found == nil || found.Kind != want.kind || !sameStrings(found.Columns, want.columns) {
			t.Fatalf("constraints = %#v, missing complete %q", constraints, name)
		}

		if want.kind == "foreign_key" {
			if found.Referenced == nil || !sameStrings(found.Referenced.Columns, want.columns) {
				t.Fatalf("foreign constraint = %#v, want referenced columns %v", found, want.columns)
			}
		}
	}
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}

func hasDescribedColumn(columns []execution.Column, name string) bool {
	for _, column := range columns {
		if column.Name == name {
			return column.Description != nil
		}
	}

	return false
}

func hasConstraint(constraints []execution.Constraint, name, kind string) bool {
	for _, constraint := range constraints {
		if constraint.Name == name && constraint.Kind == kind {
			return true
		}
	}

	return false
}

func execIntegrationSQL(t *testing.T, admin *pgx.Conn, statement string) {
	t.Helper()

	if _, err := admin.Exec(t.Context(), statement); err != nil {
		t.Fatalf("integration SQL failed: %v", err)
	}
}

func integrationIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func integrationLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func assertIntegrationSecretsAbsent(t *testing.T, data []byte, values ...string) {
	t.Helper()

	if containsSensitiveValue(data, values...) {
		t.Fatal("integration output contains sensitive value")
	}
}

func containsSensitiveValue(data []byte, values ...string) bool {
	for _, value := range values {
		if value != "" && strings.Contains(string(data), value) {
			return true
		}
	}

	return false
}

func readerConnectionString(t *testing.T, dsn, role, password string) string {
	t.Helper()

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	parsed.User = url.UserPassword(role, password)

	return parsed.String()
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(
		t.Context(),
		"tcp",
		"127.0.0.1:0",
	)
	if err != nil {
		t.Fatalf("net.ListenConfig.Listen() error = %v", err)
	}

	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}

	return address
}

func waitForHealth(t *testing.T, address string) {
	t.Helper()

	client := &http.Client{Timeout: 100 * time.Millisecond}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://"+address+"/healthz",
			nil,
		)
		if err != nil {
			t.Fatalf("http.NewRequestWithContext() error = %v", err)
		}

		response, err := client.Do(request)
		if err == nil {
			statusCode := response.StatusCode
			if err := response.Body.Close(); err != nil {
				t.Errorf("health response close error = %v", err)

				return
			}

			if statusCode == http.StatusOK {
				return
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("health endpoint %q did not become ready", address)
}
