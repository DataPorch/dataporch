//go:build integration

package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/DataPorch/dataporch/internal/connection"
	"github.com/DataPorch/dataporch/internal/connection/mysql"
	"github.com/DataPorch/dataporch/internal/execution"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

//nolint:funlen,gocyclo,wsl_v5,paralleltest // The acceptance flow is long; PostgreSQL role grants share one catalog row.
func TestQueryImportToMCPPostgresIntegration(t *testing.T) {
	harness := newIntegrationHarness(t)
	table := integrationIdentifier(harness.names.accessibleSchema) + "." +
		integrationIdentifier(harness.names.ordinaryTable)
	codeA := "query_result_cell_canary_a"
	codeB := "query_result_cell_canary_b"

	_, err := harness.admin.Exec(
		t.Context(),
		fmt.Sprintf("INSERT INTO %s (code, amount) VALUES ($1, $2), ($3, $4)", table),
		codeA,
		"10.50",
		codeB,
		"20.25",
	)
	if err != nil {
		t.Fatalf("inserting query fixture rows: %v", err)
	}

	query := fmt.Sprintf(
		"SELECT code::text AS code, amount::text AS amount FROM %s WHERE code::text IN (%s, %s) ORDER BY code",
		table,
		integrationLiteral(codeA),
		integrationLiteral(codeB),
	)
	before := harness.logs.Len()
	result := callRelationalQueryTool(t, harness.session, map[string]any{
		"kind":      "postgres",
		"source_id": harness.names.sourceID,
		"query":     query,
	})
	if result.IsError {
		t.Fatalf("query result = %#v, want success", result)
	}

	if len(result.Content) != 1 {
		t.Fatalf("query content count = %d, want one", len(result.Content))
	}
	textContent, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("query content type = %T, want TextContent", result.Content[0])
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("Marshal(query structured content) error = %v", err)
	}
	if string(encoded) != textContent.Text {
		t.Fatalf("query structured/text mismatch: %s != %s", encoded, textContent.Text)
	}

	var output execution.RelationalQueryResult
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("Unmarshal(query result) error = %v", err)
	}
	if output.Kind != "postgres" || output.SourceID != harness.names.sourceID ||
		!sameQueryColumns(output.Columns, []execution.RelationalQueryColumn{
			{Name: "code", DatabaseType: "text"},
			{Name: "amount", DatabaseType: "text"},
		}) || output.RowCount != 2 || output.Truncated ||
		!sameQueryRows(output.Rows, [][]*string{{&codeA, stringPointer("10.50")}, {&codeB, stringPointer("20.25")}}) {
		t.Fatalf("query output = %#v, want ordered fixture rows", output)
	}

	successLog := harness.logs.String()[before:]
	if strings.Count(successLog, "relational query completed") != 1 ||
		!strings.Contains(successLog, fmt.Sprintf("query_size_bytes=%d", len(query))) ||
		!strings.Contains(successLog, "kind=postgres") ||
		!strings.Contains(successLog, "source_id="+string(harness.names.sourceID)) ||
		!strings.Contains(successLog, "row_count=2") ||
		strings.Contains(successLog, "rows=") || strings.Contains(successLog, "cells=") {
		t.Fatalf("success query log = %q", successLog)
	}

	insertQuery := fmt.Sprintf(
		"INSERT INTO %s (code, amount) VALUES (%s, 30.75)",
		table,
		integrationLiteral("query_read_only_canary"),
	)
	insertFailure := callRelationalQueryFailureTool(t, harness.session, insertQuery, harness.names.sourceID)
	if insertFailure.Category != execution.ErrorCategoryReadOnlyViolation ||
		insertFailure.DatabaseError == nil || insertFailure.DatabaseError.Kind != "postgres" ||
		insertFailure.DatabaseError.Code != "25006" || insertFailure.DatabaseError.Message == "" {
		t.Fatalf("insert failure = %#v, want read-only PostgreSQL error", insertFailure)
	}

	deniedQuery := "SELECT id FROM " + integrationIdentifier(harness.names.deniedSchema) + "." +
		integrationIdentifier(harness.names.deniedTable)
	deniedFailure := callRelationalQueryFailureTool(t, harness.session, deniedQuery, harness.names.sourceID)
	if deniedFailure.Category != execution.ErrorCategoryDatabasePermissionDenied ||
		deniedFailure.DatabaseError == nil || deniedFailure.DatabaseError.Code != "42501" {
		t.Fatalf("denied failure = %#v, want database_permission_denied", deniedFailure)
	}

	undefinedQuery := "SELECT dataporch_query_undefined_column"
	undefinedFailure := callRelationalQueryFailureTool(t, harness.session, undefinedQuery, harness.names.sourceID)
	if undefinedFailure.Category != execution.ErrorCategoryInvalidQuery ||
		undefinedFailure.DatabaseError == nil || undefinedFailure.DatabaseError.Code != "42703" ||
		undefinedFailure.DatabaseError.Kind != "postgres" || undefinedFailure.DatabaseError.Message == "" ||
		undefinedFailure.DatabaseError.Severity == "" || undefinedFailure.DatabaseError.File == "" ||
		undefinedFailure.DatabaseError.Line == 0 || undefinedFailure.DatabaseError.Routine == "" {
		t.Fatalf("undefined-column failure = %#v, want complete 42703 fields", undefinedFailure)
	}

	logs := harness.logs.String()
	if strings.Count(logs, "relational query failed") < 3 ||
		!strings.Contains(logs, fmt.Sprintf("query_size_bytes=%d", len(insertQuery))) ||
		!strings.Contains(logs, fmt.Sprintf("query_size_bytes=%d", len(deniedQuery))) ||
		!strings.Contains(logs, fmt.Sprintf("query_size_bytes=%d", len(undefinedQuery))) ||
		strings.Contains(logs, insertQuery) || strings.Contains(logs, deniedQuery) || strings.Contains(logs, undefinedQuery) {
		t.Fatalf("query failure logs = %q", logs)
	}

	observed, err := json.Marshal(struct {
		Output         execution.RelationalQueryResult
		InsertFailure  execution.Failure
		DeniedFailure  execution.Failure
		UndefinedError execution.Failure
	}{
		Output:         output,
		InsertFailure:  insertFailure,
		DeniedFailure:  deniedFailure,
		UndefinedError: undefinedFailure,
	})
	if err != nil {
		t.Fatalf("Marshal(query observations) error = %v", err)
	}
	assertIntegrationSecretsAbsent(t, observed, harness.dsn, harness.readerDSN, harness.names.password, harness.names.role)
	assertIntegrationSecretsAbsent(t, []byte(logs), harness.dsn, harness.readerDSN, harness.names.password, harness.names.role)
}

func callRelationalQueryTool(
	t *testing.T,
	session *mcpsdk.ClientSession,
	arguments map[string]any,
) *mcpsdk.CallToolResult {
	t.Helper()

	result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "relational_database.query",
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("CallTool(relational_database.query) error = %v", err)
	}

	return result
}

func TestQueryImportToMCPMySQLIntegration(t *testing.T) {
	t.Parallel()

	session, sourceID, _ := newMySQLAppIntegrationSession(t)

	result := callRelationalQueryTool(t, session, map[string]any{
		"kind": string(mysql.Kind), "source_id": sourceID, "query": "SELECT 1 AS value",
	})
	if result.IsError {
		t.Fatalf("query result = %#v, want success", result)
	}

	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("Marshal(query structured content) error = %v", err)
	}

	var output execution.RelationalQueryResult
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("Unmarshal(query result) error = %v", err)
	}

	if output.Kind != mysql.Kind || output.SourceID != sourceID || output.RowCount != 1 || len(output.Rows) != 1 {
		t.Fatalf("query output = %#v", output)
	}
}

func callRelationalQueryFailureTool(
	t *testing.T,
	session *mcpsdk.ClientSession,
	query string,
	sourceID connection.ID,
) execution.Failure {
	t.Helper()

	result := callRelationalQueryTool(t, session, map[string]any{
		"kind":      "postgres",
		"source_id": sourceID,
		"query":     query,
	})
	if !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("query failure result = %#v, want one safe error content item", result)
	}

	textContent, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("query failure content type = %T, want TextContent", result.Content[0])
	}

	var failure execution.Failure
	if err := json.Unmarshal([]byte(textContent.Text), &failure); err != nil {
		t.Fatalf("Unmarshal(query failure) error = %v", err)
	}

	return failure
}

func sameQueryColumns(got, want []execution.RelationalQueryColumn) bool {
	if len(got) != len(want) {
		return false
	}

	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}

	return true
}

func sameQueryRows(got, want [][]*string) bool {
	if len(got) != len(want) {
		return false
	}

	for rowIndex := range want {
		if len(got[rowIndex]) != len(want[rowIndex]) {
			return false
		}

		for columnIndex := range want[rowIndex] {
			if (got[rowIndex][columnIndex] == nil) != (want[rowIndex][columnIndex] == nil) {
				return false
			}

			if got[rowIndex][columnIndex] != nil && *got[rowIndex][columnIndex] != *want[rowIndex][columnIndex] {
				return false
			}
		}
	}

	return true
}

func stringPointer(value string) *string {
	return &value
}
