//go:build integration

package app

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DataPorch/dataporch/internal/execution"
	"github.com/DataPorch/dataporch/internal/mcptoken"
	mcpTokenLocal "github.com/DataPorch/dataporch/internal/mcptoken/local"
	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

//nolint:gocyclo,funlen // This end-to-end scenario covers import, query, replacement, and redaction together.
func TestSQLiteImportToMCPIntegration(t *testing.T) {
	t.Parallel()

	fixturePath := filepath.Join(t.TempDir(), "fixture.db")
	createSQLiteAppFixture(t, fixturePath)

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

	mcpToken, _, err := tokenService.Create(t.Context())
	if err != nil {
		t.Fatalf("tokenService.Create() error = %v", err)
	}

	logs := &strings.Builder{}

	application, err := New(cfg, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	startIntegrationApplication(t, application, cfg.AdminSocketPath, cfg.HTTPAddress)

	connectionURI := "sqlite://" + (&url.URL{Path: fixturePath}).EscapedPath()

	response, err := importOverSocket(cfg.AdminSocketPath, "sqlite-fixture", "sqlite", connectionURI)
	if err != nil {
		t.Fatalf("importOverSocket() error = %v", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Errorf("response body close error = %v", closeErr)
		}
	}()

	var importResult struct {
		DatabaseID         string `json:"databaseId"`
		IsConnectionTested bool   `json:"connectionTested"`
	}
	if err := json.NewDecoder(response.Body).Decode(&importResult); err != nil {
		t.Fatalf("decode import response: %v", err)
	}

	if response.StatusCode != http.StatusCreated || importResult.DatabaseID != "sqlite-fixture" || importResult.IsConnectionTested {
		t.Fatalf("import response = %#v status=%d, want untested created source", importResult, response.StatusCode)
	}

	session := connectIntegrationMCP(t, cfg.HTTPAddress, mcpToken)

	source := callDiscoveryTool[execution.ListDataSourcesResult](t, session, "data_source.list", nil)
	if len(source.Sources) != 1 || source.Sources[0].Kind != "sqlite" {
		t.Fatalf("data sources = %#v, want one sqlite source", source.Sources)
	}

	schemas := callDiscoveryTool[execution.ListRelationalSchemasResult](t, session, "relational_database.list_schemas", map[string]any{
		"source_id": "sqlite-fixture",
	})
	if len(schemas.Schemas) != 1 || schemas.Schemas[0].Name != "main" {
		t.Fatalf("schemas = %#v, want main", schemas.Schemas)
	}

	tables := callDiscoveryTool[execution.ListRelationalTablesResult](t, session, "relational_database.list_tables", map[string]any{
		"source_id": "sqlite-fixture",
		"schema":    "main",
		"limit":     10,
	})
	if !hasSQLiteTable(tables.Tables, "items", execution.RelationKindTable) ||
		!hasSQLiteTable(tables.Tables, "docs", execution.RelationKindVirtualTable) {
		t.Fatalf("tables = %#v, want ordinary and virtual SQLite tables", tables.Tables)
	}

	columns := callDiscoveryTool[execution.ListRelationalColumnsResult](t, session, "relational_database.list_columns", map[string]any{
		"source_id": "sqlite-fixture",
		"schema":    "main",
		"table":     "items",
		"limit":     10,
	})
	if columns.RelationKind != execution.RelationKindTable || !hasSQLiteColumn(columns.Columns, "payload", execution.TypeAffinityBlob) {
		t.Fatalf("columns = %#v, want table payload BLOB metadata", columns)
	}

	if !hasSQLiteGeneratedColumn(columns.Columns, "generated", "stored") {
		t.Fatalf("columns = %#v, want stored generated metadata", columns.Columns)
	}

	if !hasSQLiteConstraint(columns.Constraints, "primary_key") || !hasSQLiteConstraint(columns.Constraints, "unique") || !hasSQLiteConstraint(columns.Constraints, "foreign_key") {
		t.Fatalf("constraints = %#v, want primary, unique, and foreign keys", columns.Constraints)
	}

	query := "SELECT code, payload FROM items ORDER BY id"
	queryStart := logs.Len()

	queryResult := callRelationalQueryTool(t, session, map[string]any{
		"kind":      "sqlite",
		"source_id": "sqlite-fixture",
		"query":     query,
	})
	if queryResult.IsError || len(queryResult.Content) != 1 {
		t.Fatalf("query result = %#v, want one successful content item", queryResult)
	}

	encoded, err := json.Marshal(queryResult.StructuredContent)
	if err != nil {
		t.Fatalf("Marshal(query structured content) error = %v", err)
	}

	var output execution.RelationalQueryResult
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("Unmarshal(query output) error = %v", err)
	}

	if output.Kind != "sqlite" || output.RowCount != 1 || len(output.Rows) != 1 || output.Rows[0][1] == nil || *output.Rows[0][1] != "X'0001FF'" {
		t.Fatalf("query output = %#v, want SQLite BLOB literal", output)
	}

	if queryLog := logs.String()[queryStart:]; !strings.Contains(queryLog, "kind=sqlite") || strings.Contains(queryLog, query) {
		t.Fatalf("query log = %q, want kind and no SQL text", queryLog)
	}

	failure := callDiscoveryToolFailure(t, session, "relational_database.query", map[string]any{
		"kind":      "sqlite",
		"source_id": "sqlite-fixture",
		"query":     "SELECT missing_column FROM items",
	})
	if failure.Category != execution.ErrorCategoryInvalidQuery || failure.DatabaseError == nil || failure.DatabaseError.Kind != "sqlite" || failure.DatabaseError.Code != "SQLITE_ERROR" || strings.Contains(failure.DatabaseError.Message, fixturePath) {
		t.Fatalf("query failure = %#v, want safe SQLite invalid-query error", failure)
	}

	replacementPath := filepath.Join(t.TempDir(), "replacement.db")
	createSQLiteAppFixture(t, replacementPath)
	updateSQLiteFixtureCode(t, replacementPath, "replacement")
	replacementURI := "sqlite://" + (&url.URL{Path: replacementPath}).EscapedPath()

	replacementResponse, err := importOverSocket(cfg.AdminSocketPath, "sqlite-fixture", "sqlite", replacementURI)
	if err != nil {
		t.Fatalf("replacement import error = %v", err)
	}

	if replacementResponse.Body != nil {
		_ = replacementResponse.Body.Close()
	}

	if replacementResponse.StatusCode != http.StatusOK {
		t.Fatalf("replacement import status = %d, want 200", replacementResponse.StatusCode)
	}

	replacementQuery := callRelationalQueryTool(t, session, map[string]any{
		"kind":      "sqlite",
		"source_id": "sqlite-fixture",
		"query":     query,
	})

	replacementEncoded, err := json.Marshal(replacementQuery.StructuredContent)
	if err != nil {
		t.Fatalf("Marshal(replacement query) error = %v", err)
	}

	var replacementOutput execution.RelationalQueryResult
	if err := json.Unmarshal(replacementEncoded, &replacementOutput); err != nil {
		t.Fatalf("Unmarshal(replacement query) error = %v", err)
	}

	if len(replacementOutput.Rows) != 1 || replacementOutput.Rows[0][0] == nil || *replacementOutput.Rows[0][0] != "replacement" {
		t.Fatalf("replacement query output = %#v, want replacement row", replacementOutput)
	}

	failedResponse, err := importOverSocket(cfg.AdminSocketPath, "sqlite-fixture", "sqlite", "sqlite://relative/path")
	if err != nil {
		t.Fatalf("failed replacement import request error = %v", err)
	}

	if failedResponse.Body != nil {
		_ = failedResponse.Body.Close()
	}

	if failedResponse.StatusCode == http.StatusCreated || failedResponse.StatusCode == http.StatusOK {
		t.Fatalf("failed replacement import status = %d, want failure without registration", failedResponse.StatusCode)
	}

	if strings.Contains(logs.String(), "SELECT missing_column FROM items") {
		t.Fatalf("logs contain private SQL text: %q", logs.String())
	}
}

func createSQLiteAppFixture(t *testing.T, path string) {
	t.Helper()

	conn, err := sqlite3.OpenFlags(path, sqlite3.OPEN_READWRITE|sqlite3.OPEN_CREATE|sqlite3.OPEN_URI)
	if err != nil {
		t.Fatalf("sqlite fixture open: %v", err)
	}

	if err := fts5.Register(conn); err != nil {
		_ = conn.Close()

		t.Fatalf("sqlite fts5 register: %v", err)
	}

	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE parents (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE items (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parents(id) ON DELETE SET NULL, code TEXT NOT NULL UNIQUE, payload BLOB, generated TEXT GENERATED ALWAYS AS (code || '-generated') STORED)`,
		`INSERT INTO parents(id) VALUES (1)`,
		`INSERT INTO items(parent_id, code, payload) VALUES (1, 'fixture', X'0001FF')`,
		`CREATE VIRTUAL TABLE docs USING fts5(content)`,
		`INSERT INTO docs(content) VALUES ('virtual fixture')`,
	}
	for _, statement := range statements {
		if err := conn.Exec(statement); err != nil {
			_ = conn.Close()

			t.Fatalf("sqlite fixture Exec(%q): %v", statement, err)
		}
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("sqlite fixture close: %v", err)
	}
}

func updateSQLiteFixtureCode(t *testing.T, path, code string) {
	t.Helper()

	conn, err := sqlite3.OpenFlags(path, sqlite3.OPEN_READWRITE|sqlite3.OPEN_URI)
	if err != nil {
		t.Fatalf("sqlite replacement open: %v", err)
	}

	if err := conn.Exec(fmt.Sprintf("UPDATE items SET code = '%s' WHERE id = 1", code)); err != nil {
		_ = conn.Close()

		t.Fatalf("sqlite replacement update: %v", err)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("sqlite replacement close: %v", err)
	}
}

func hasSQLiteTable(tables []execution.Table, name string, kind execution.RelationKind) bool {
	for _, table := range tables {
		if table.Name == name && table.Kind == kind {
			return true
		}
	}

	return false
}

func hasSQLiteColumn(columns []execution.Column, name string, affinity execution.TypeAffinity) bool {
	for _, column := range columns {
		if column.Name == name && column.Type.Affinity == affinity {
			return true
		}
	}

	return false
}

func hasSQLiteGeneratedColumn(columns []execution.Column, name, kind string) bool {
	for _, column := range columns {
		if column.Name == name && column.Generated != nil && column.Generated.Kind == kind {
			return true
		}
	}

	return false
}

func hasSQLiteConstraint(constraints []execution.Constraint, kind string) bool {
	for _, constraint := range constraints {
		if constraint.Kind == kind {
			return true
		}
	}

	return false
}
