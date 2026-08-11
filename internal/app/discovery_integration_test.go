//go:build integration

package app

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/connection/postgres"
	"github.com/adamraziv/dataporch/internal/execution"
	"github.com/jackc/pgx/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiscoveryImportToMCPPostgresIntegration(t *testing.T) {
	t.Parallel()

	dsn := os.Getenv("DATAPORCH_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DATAPORCH_TEST_POSTGRES_DSN is not set")
	}

	names := integrationNames(t)
	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	t.Cleanup(func() { cleanupIntegrationDatabase(t, admin, names) })
	foreignAvailable := setupIntegrationDatabase(t, admin, names)

	cfg := testConfigFor(t)
	cfg.ResourceLimit = 3
	cfg.HTTPAddress = freeTCPAddress(t)
	if err := InitializeSecrets(cfg); err != nil {
		t.Fatalf("InitializeSecrets() error = %v", err)
	}

	var logs strings.Builder
	var runtime *countingPostgresRuntime
	application, err := newWithDependencies(cfg, slog.New(slog.NewTextHandler(&logs, nil)), appDependencies{
		adapters: []connection.Adapter{postgres.New()},
		newPostgresRuntime: func(preparer postgres.DefinitionPreparer) (postgresRuntime, error) {
			opener, err := postgres.NewOpener(preparer)
			if err != nil {
				return nil, err
			}
			runtime = &countingPostgresRuntime{opener: opener}
			return runtime, nil
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	appContext, cancel := context.WithCancel(t.Context())
	serverDone := make(chan error, 1)
	go func() { serverDone <- application.Run(appContext) }()
	t.Cleanup(func() {
		cancel()
		if err := <-serverDone; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})

	waitForFile(t, cfg.AdminSocketPath)
	waitForHealth(t, cfg.HTTPAddress)

	readerDSN := readerConnectionString(t, dsn, names.role, names.password)
	reader, err := pgx.Connect(t.Context(), readerDSN)
	if err != nil {
		t.Fatalf("reader pgx.Connect() error = %v", err)
	}
	if err := reader.Close(t.Context()); err != nil {
		t.Fatalf("reader.Close() error = %v", err)
	}
	response, err := importOverSocket(cfg.AdminSocketPath, string(names.sourceID), "postgres", readerDSN)
	if err != nil {
		t.Fatalf("importOverSocket() error = %v", err)
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("import status = %d, want %d", response.StatusCode, http.StatusCreated)
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "dataporch-integration", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{
		Endpoint:             "http://" + cfg.HTTPAddress + "/mcp",
		HTTPClient:           &http.Client{Timeout: 30 * time.Second},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("MCP Connect() error = %v", err)
	}
	defer session.Close()

	sources := callDiscoveryTool[execution.ListDataSourcesResult](t, session, "data_source.list", nil)
	if len(sources.Sources) != 1 || sources.Sources[0].ID != names.sourceID {
		t.Fatalf("data sources = %#v, want imported source %q", sources.Sources, names.sourceID)
	}
	if len(sources.Sources[0].Capabilities) != 1 || sources.Sources[0].Capabilities[0] != execution.CapabilityRelationalDatabase {
		t.Fatalf("source capabilities = %#v, want relational_database", sources.Sources[0].Capabilities)
	}
	if got := runtime.openCount(); got != 0 {
		t.Fatalf("opens after data_source.list = %d, want zero", got)
	}

	schemaRequest := map[string]any{
		"source_id": names.sourceID,
		"search":    names.suffix,
		"limit":     2,
	}
	schemas := callDiscoveryTool[execution.ListRelationalSchemasResult](t, session, "relational_database.list_schemas", schemaRequest)
	allSchemas := append([]execution.Schema(nil), schemas.Schemas...)
	if schemas.NextCursor != "" {
		schemaRequest["cursor"] = schemas.NextCursor
		nextSchemas := callDiscoveryTool[execution.ListRelationalSchemasResult](t, session, "relational_database.list_schemas", schemaRequest)
		allSchemas = append(allSchemas, nextSchemas.Schemas...)
	}
	if len(allSchemas) != 3 {
		t.Fatalf("schemas = %#v, want three accessible schemas", allSchemas)
	}
	assertSchemaNames(t, allSchemas, names.accessibleSchema, names.mixedSchema, names.secondarySchema)
	if runtime.openCount() == 0 {
		t.Fatal("relational schema discovery did not open the database")
	}
	for _, schema := range allSchemas {
		if schema.Description != nil {
			t.Fatalf("schema description returned without flag: %#v", schema)
		}
	}
	describedSchemas := callDiscoveryTool[execution.ListRelationalSchemasResult](t, session, "relational_database.list_schemas", map[string]any{
		"source_id":            names.sourceID,
		"search":               names.accessibleSchema,
		"include_descriptions": true,
	})
	if len(describedSchemas.Schemas) != 1 || describedSchemas.Schemas[0].Description == nil {
		t.Fatalf("described schemas = %#v, want requested schema description", describedSchemas.Schemas)
	}

	tableRequest := map[string]any{
		"source_id": names.sourceID,
		"schema":    names.accessibleSchema,
		"limit":     3,
	}
	tables := callDiscoveryTool[execution.ListRelationalTablesResult](t, session, "relational_database.list_tables", tableRequest)
	allTables := append([]execution.Table(nil), tables.Tables...)
	for tables.NextCursor != "" {
		tableRequest["cursor"] = tables.NextCursor
		tables = callDiscoveryTool[execution.ListRelationalTablesResult](t, session, "relational_database.list_tables", tableRequest)
		allTables = append(allTables, tables.Tables...)
	}
	assertTableKind(t, allTables, names.ordinaryTable, execution.RelationKindTable)
	assertTableKind(t, allTables, names.partitionedTable, execution.RelationKindPartitionedTable)
	assertTableKind(t, allTables, names.view, execution.RelationKindView)
	assertTableKind(t, allTables, names.materializedView, execution.RelationKindMaterializedView)
	if hasTable(allTables, names.deniedTable) {
		t.Fatalf("tables include unreadable relation %q: %#v", names.deniedTable, allTables)
	}
	if foreignAvailable {
		assertTableKind(t, allTables, names.foreignTable, execution.RelationKindForeignTable)
	}
	for _, table := range allTables {
		if table.Description != nil {
			t.Fatalf("table description returned without flag: %#v", table)
		}
	}
	describedTables := callDiscoveryTool[execution.ListRelationalTablesResult](t, session, "relational_database.list_tables", map[string]any{
		"source_id":            names.sourceID,
		"schema":               names.accessibleSchema,
		"search":               names.ordinaryTable,
		"include_descriptions": true,
	})
	if len(describedTables.Tables) != 1 || describedTables.Tables[0].Description == nil {
		t.Fatalf("described tables = %#v, want requested table description", describedTables.Tables)
	}

	mixedTables := callDiscoveryTool[execution.ListRelationalTablesResult](t, session, "relational_database.list_tables", map[string]any{
		"source_id": names.sourceID,
		"schema":    names.mixedSchema,
	})
	if !hasTable(mixedTables.Tables, names.mixedTable) {
		t.Fatalf("mixed-case tables = %#v, want %q", mixedTables.Tables, names.mixedTable)
	}
	columnRequest := map[string]any{
		"source_id": names.sourceID,
		"schema":    names.accessibleSchema,
		"table":     names.ordinaryTable,
		"limit":     3,
	}
	columns := callDiscoveryTool[execution.ListRelationalColumnsResult](t, session, "relational_database.list_columns", columnRequest)
	allColumns := append([]execution.Column(nil), columns.Columns...)
	allConstraints := append([]execution.Constraint(nil), columns.Constraints...)
	for columns.NextCursor != "" {
		columnRequest["cursor"] = columns.NextCursor
		columns = callDiscoveryTool[execution.ListRelationalColumnsResult](t, session, "relational_database.list_columns", columnRequest)
		allColumns = append(allColumns, columns.Columns...)
		allConstraints = append(allConstraints, columns.Constraints...)
	}
	if len(allColumns) < 8 {
		t.Fatalf("columns = %#v, want metadata-rich ordinary table", allColumns)
	}
	if len(allConstraints) < 3 {
		t.Fatalf("constraints = %#v, want primary, foreign, and check constraints", allConstraints)
	}
	if !hasColumn(allColumns, "generated_amount") {
		t.Fatalf("columns omit generated column: %#v", allColumns)
	}
	assertColumnMetadata(t, allColumns)
	if !hasConstraint(allConstraints, "orders_"+names.suffix+"_pkey", "primary_key") || !hasConstraint(allConstraints, "orders_"+names.suffix+"_customer_id_fkey", "foreign_key") {
		t.Fatalf("constraints omit expected keys: %#v", allConstraints)
	}
	if !hasConstraint(allConstraints, "orders_"+names.suffix+"_amount_check", "check") {
		t.Fatalf("constraints omit check constraint: %#v", allConstraints)
	}
	for _, column := range allColumns {
		if column.Description != nil {
			t.Fatalf("column description returned without flag: %#v", column)
		}
	}
	describedColumns := callDiscoveryTool[execution.ListRelationalColumnsResult](t, session, "relational_database.list_columns", map[string]any{
		"source_id":            names.sourceID,
		"schema":               names.accessibleSchema,
		"table":                names.ordinaryTable,
		"include_descriptions": true,
	})
	if !hasDescribedColumn(describedColumns.Columns, "amount") {
		t.Fatalf("described columns = %#v, want amount description", describedColumns.Columns)
	}

	literalSearch := callDiscoveryTool[execution.ListRelationalTablesResult](t, session, "relational_database.list_tables", map[string]any{
		"source_id": names.sourceID,
		"schema":    names.accessibleSchema,
		"search":    `%_*`,
	})
	if len(literalSearch.Tables) != 0 {
		t.Fatalf("literal search tables = %#v, want no wildcard interpretation", literalSearch.Tables)
	}

	compositeRequest := map[string]any{
		"source_id": names.sourceID,
		"schema":    names.accessibleSchema,
		"table":     names.compositeChild,
		"limit":     3,
	}
	compositeColumns := callDiscoveryTool[execution.ListRelationalColumnsResult](t, session, "relational_database.list_columns", compositeRequest)
	compositeConstraints := append([]execution.Constraint(nil), compositeColumns.Constraints...)
	for compositeColumns.NextCursor != "" {
		compositeRequest["cursor"] = compositeColumns.NextCursor
		compositeColumns = callDiscoveryTool[execution.ListRelationalColumnsResult](t, session, "relational_database.list_columns", compositeRequest)
		compositeConstraints = append(compositeConstraints, compositeColumns.Constraints...)
	}
	assertCompositeConstraints(t, compositeConstraints, names)

	columnGrant := callDiscoveryTool[execution.ListRelationalColumnsResult](t, session, "relational_database.list_columns", map[string]any{
		"source_id": names.sourceID,
		"schema":    names.accessibleSchema,
		"table":     names.columnGrantTable,
	})
	if len(columnGrant.Columns) != 1 || columnGrant.Columns[0].Name != "visible_value" {
		t.Fatalf("column-grant columns = %#v, want only visible_value", columnGrant.Columns)
	}

	missingFailure := callDiscoveryToolFailure(t, session, "relational_database.list_tables", map[string]any{
		"source_id": names.sourceID,
		"schema":    "missing_" + names.suffix,
	})
	if missingFailure.Category != execution.ErrorCategorySchemaNotFound {
		t.Fatalf("missing schema failure = %#v, want schema_not_found", missingFailure)
	}
	deniedFailure := callDiscoveryToolFailure(t, session, "relational_database.list_tables", map[string]any{
		"source_id": names.sourceID,
		"schema":    names.deniedSchema,
	})
	if deniedFailure.Category != execution.ErrorCategoryDatabasePermissionDenied {
		t.Fatalf("denied schema failure = %#v, want database_permission_denied", deniedFailure)
	}
	missingRelationFailure := callDiscoveryToolFailure(t, session, "relational_database.list_columns", map[string]any{
		"source_id": names.sourceID,
		"schema":    names.accessibleSchema,
		"table":     "missing_" + names.suffix,
	})
	if missingRelationFailure.Category != execution.ErrorCategoryRelationNotFound {
		t.Fatalf("missing relation failure = %#v, want relation_not_found", missingRelationFailure)
	}
	deniedRelationFailure := callDiscoveryToolFailure(t, session, "relational_database.list_columns", map[string]any{
		"source_id": names.sourceID,
		"schema":    names.accessibleSchema,
		"table":     names.deniedTable,
	})
	if deniedRelationFailure.Category != execution.ErrorCategoryDatabasePermissionDenied {
		t.Fatalf("denied relation failure = %#v, want database_permission_denied", deniedRelationFailure)
	}

	observed, err := json.Marshal(struct {
		Sources     execution.ListDataSourcesResult
		Schemas     []execution.Schema
		Tables      []execution.Table
		Columns     []execution.Column
		Constraints []execution.Constraint
	}{sources, allSchemas, allTables, allColumns, allConstraints})
	if err != nil {
		t.Fatalf("Marshal(observed) error = %v", err)
	}
	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgx.ParseConfig() error = %v", err)
	}
	assertIntegrationSecretsAbsent(t, observed, dsn, readerDSN, adminConfig.User, adminConfig.Database, fmt.Sprint(adminConfig.Port), names.password, names.role)
	assertIntegrationSecretsAbsent(t, []byte(logs.String()), dsn, readerDSN, adminConfig.User, adminConfig.Database, fmt.Sprint(adminConfig.Port), names.password, names.role)
	if runtime.openCount() == 0 {
		t.Fatal("relational discovery did not increment opener count")
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
	deniedSchema     string
	ordinaryTable    string
	partitionedTable string
	view             string
	materializedView string
	deniedTable      string
	columnGrantTable string
	mixedTable       string
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
		deniedSchema:     "dp_denied_" + suffix,
		ordinaryTable:    "orders_" + suffix,
		partitionedTable: "events_" + suffix,
		view:             "orders_view_" + suffix,
		materializedView: "orders_mv_" + suffix,
		deniedTable:      "hidden_" + suffix,
		columnGrantTable: "column_grant_" + suffix,
		mixedTable:       "MixedOrders_" + suffix,
		foreignTable:     "foreign_" + suffix,
		server:           "dp_server_" + suffix,
		compositeParent:  "composite_parent_" + suffix,
		compositeChild:   "composite_child_" + suffix,
	}
}

func setupIntegrationDatabase(t *testing.T, admin *pgx.Conn, names integrationDatabaseNames) bool {
	t.Helper()

	accessible := integrationIdentifier(names.accessibleSchema)
	role := integrationIdentifier(names.role)
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD %s", role, integrationLiteral(names.password)))
	adminConfig, err := pgx.ParseConfig(os.Getenv("DATAPORCH_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatalf("pgx.ParseConfig() error = %v", err)
	}
	execIntegrationSQL(t, admin, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", integrationIdentifier(adminConfig.Database), role))
	for _, schema := range []string{names.accessibleSchema, names.secondarySchema, names.mixedSchema, names.deniedSchema} {
		execIntegrationSQL(t, admin, fmt.Sprintf("CREATE SCHEMA %s", integrationIdentifier(schema)))
	}
	execIntegrationSQL(t, admin, fmt.Sprintf("GRANT USAGE ON SCHEMA %s, %s, %s TO %s", accessible, integrationIdentifier(names.secondarySchema), integrationIdentifier(names.mixedSchema), role))

	customers := accessible + "." + integrationIdentifier("customers_"+names.suffix)
	orders := accessible + "." + integrationIdentifier(names.ordinaryTable)
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE TYPE %s.order_state AS ENUM ('open', 'closed')", accessible))
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE DOMAIN %s.customer_code AS text CHECK (VALUE <> '')", accessible))
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE TABLE %s (id bigint PRIMARY KEY, name text UNIQUE)", customers))
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE TABLE %s (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, customer_id bigint REFERENCES %s(id), amount numeric(12,2) NOT NULL DEFAULT 0 CHECK (amount >= 0), code %s.customer_code, state %s.order_state DEFAULT 'open', tags text[], created_at timestamp(3), generated_amount numeric GENERATED ALWAYS AS (amount * 2) STORED)", orders, customers, accessible, accessible))
	compositeParent := accessible + "." + integrationIdentifier(names.compositeParent)
	compositeChild := accessible + "." + integrationIdentifier(names.compositeChild)
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE TABLE %s (tenant_id bigint NOT NULL, parent_id bigint NOT NULL, code text NOT NULL, CONSTRAINT %s PRIMARY KEY (tenant_id, parent_id), CONSTRAINT %s UNIQUE (tenant_id, code))", compositeParent, integrationIdentifier(names.compositeParent+"_pkey"), integrationIdentifier(names.compositeParent+"_unique")))
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE TABLE %s (tenant_id bigint NOT NULL, parent_id bigint NOT NULL, code text NOT NULL, amount numeric NOT NULL, CONSTRAINT %s PRIMARY KEY (tenant_id, parent_id), CONSTRAINT %s UNIQUE (tenant_id, code), CONSTRAINT %s FOREIGN KEY (tenant_id, parent_id) REFERENCES %s (tenant_id, parent_id), CONSTRAINT %s CHECK (amount >= 0))", compositeChild, integrationIdentifier(names.compositeChild+"_pkey"), integrationIdentifier(names.compositeChild+"_unique"), integrationIdentifier(names.compositeChild+"_parent_fkey"), compositeParent, integrationIdentifier(names.compositeChild+"_amount_check")))
	execIntegrationSQL(t, admin, fmt.Sprintf("COMMENT ON SCHEMA %s IS 'schema description'", accessible))
	execIntegrationSQL(t, admin, fmt.Sprintf("COMMENT ON TABLE %s IS 'orders description'", orders))
	execIntegrationSQL(t, admin, fmt.Sprintf("COMMENT ON COLUMN %s.amount IS 'amount description'", orders))

	partitioned := accessible + "." + integrationIdentifier(names.partitionedTable)
	partition := accessible + "." + integrationIdentifier(names.partitionedTable+"_2026")
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE TABLE %s (id bigint, happened_at date, PRIMARY KEY (id, happened_at)) PARTITION BY RANGE (happened_at)", partitioned))
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')", partition, partitioned))

	view := accessible + "." + integrationIdentifier(names.view)
	materialized := accessible + "." + integrationIdentifier(names.materializedView)
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE VIEW %s AS SELECT id, amount FROM %s", view, orders))
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE MATERIALIZED VIEW %s AS SELECT id, amount FROM %s", materialized, orders))
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE TABLE %s.%s (id bigint, secret_value text)", accessible, integrationIdentifier(names.deniedTable)))
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE TABLE %s.%s (visible_value text, hidden_value text)", accessible, integrationIdentifier(names.columnGrantTable)))
	execIntegrationSQL(t, admin, fmt.Sprintf("CREATE TABLE %s.%s (id bigint)", integrationIdentifier(names.mixedSchema), integrationIdentifier(names.mixedTable)))

	for _, relation := range []string{orders, customers, compositeParent, compositeChild, partitioned, partition, view, materialized, integrationIdentifier(names.mixedSchema) + "." + integrationIdentifier(names.mixedTable)} {
		execIntegrationSQL(t, admin, fmt.Sprintf("GRANT SELECT ON %s TO %s", relation, role))
	}
	execIntegrationSQL(t, admin, fmt.Sprintf("GRANT SELECT (visible_value) ON %s.%s TO %s", accessible, integrationIdentifier(names.columnGrantTable), role))
	execIntegrationSQL(t, admin, fmt.Sprintf("GRANT USAGE ON TYPE %s.order_state, %s.customer_code TO %s", accessible, accessible, role))
	foreignAvailable := true
	if err := setupOptionalForeignTable(t, admin, names); err != nil {
		foreignAvailable = false
		t.Logf("skipping optional postgres_fdw relation: %v", err)
	}
	return foreignAvailable
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
	statements := []string{
		fmt.Sprintf("CREATE SERVER %s FOREIGN DATA WRAPPER postgres_fdw OPTIONS (host %s, port %s, dbname %s)", server, integrationLiteral(parsed.Host), integrationLiteral(fmt.Sprint(parsed.Port)), integrationLiteral(parsed.Database)),
		fmt.Sprintf("CREATE USER MAPPING FOR %s SERVER %s OPTIONS (user %s, password %s)", integrationIdentifier(names.role), server, integrationLiteral(parsed.User), integrationLiteral(parsed.Password)),
		fmt.Sprintf("CREATE FOREIGN TABLE %s (id bigint) SERVER %s OPTIONS (schema_name %s, table_name %s)", foreign, server, integrationLiteral(names.accessibleSchema), integrationLiteral("customers_"+names.suffix)),
		fmt.Sprintf("GRANT USAGE ON FOREIGN SERVER %s TO %s", server, integrationIdentifier(names.role)),
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
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", integrationIdentifier(names.deniedSchema)),
		fmt.Sprintf("DROP OWNED BY %s", integrationIdentifier(names.role)),
		fmt.Sprintf("DROP ROLE IF EXISTS %s", integrationIdentifier(names.role)),
	} {
		if _, err := admin.Exec(context.Background(), statement); err != nil {
			t.Errorf("cleanup statement failed: %v", err)
		}
	}
}

func callDiscoveryTool[T any](t *testing.T, session *mcpsdk.ClientSession, name string, arguments map[string]any) T {
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

func callDiscoveryToolFailure(t *testing.T, session *mcpsdk.ClientSession, name string, arguments map[string]any) execution.Failure {
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

func (r *countingPostgresRuntime) Open(ctx context.Context, id connection.ID) (*postgres.Client, error) {
	r.opens.Add(1)
	return r.opener.Open(ctx, id)
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

func hasColumn(columns []execution.Column, name string) bool {
	for _, column := range columns {
		if column.Name == name {
			return true
		}
	}
	return false
}

func assertColumnMetadata(t *testing.T, columns []execution.Column) {
	t.Helper()

	checks := map[string]func(execution.Column) bool{
		"id": func(column execution.Column) bool {
			return column.Identity != nil && column.Identity.Generation == "always"
		},
		"amount": func(column execution.Column) bool {
			return !column.Nullable && column.DefaultExpression != nil && column.Type.Precision != nil && *column.Type.Precision == 12 && column.Type.Scale != nil && *column.Type.Scale == 2
		},
		"code": func(column execution.Column) bool {
			return column.Type.Category == execution.TypeCategoryDomain && column.Type.DomainBaseType != nil && column.Type.DomainBaseType.Name == "text"
		},
		"state": func(column execution.Column) bool {
			return column.Type.Category == execution.TypeCategoryEnum && column.DefaultExpression != nil
		},
		"tags": func(column execution.Column) bool {
			return column.Type.Category == execution.TypeCategoryArray && column.Type.IsArray && column.Type.ElementType != nil && column.Type.ElementType.Name == "text"
		},
		"created_at": func(column execution.Column) bool {
			return column.Type.TemporalPrecision != nil && *column.Type.TemporalPrecision == 3
		},
		"generated_amount": func(column execution.Column) bool {
			return column.Generated != nil && column.Generated.Kind == "stored" && strings.Contains(column.Generated.Expression, "amount")
		},
	}

	for name, check := range checks {
		found := false
		for _, column := range columns {
			if column.Name == name {
				found = true
				if !check(column) {
					t.Fatalf("column %q metadata = %#v, want structured metadata", name, column)
				}
				break
			}
		}
		if !found {
			t.Fatalf("columns = %#v, missing %q", columns, name)
		}
	}
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
	for _, value := range values {
		if value != "" && strings.Contains(string(data), value) {
			t.Fatalf("integration output contains sensitive value")
		}
	}
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
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
		response, err := client.Get("http://" + address + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("health endpoint %q did not become ready", address)
}
