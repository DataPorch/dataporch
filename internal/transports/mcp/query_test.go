package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func newMCPTestDependencies(logger *slog.Logger) Dependencies {
	return Dependencies{
		Discoverer:             &recordingDiscoverer{},
		RelationalQuerier:      &recordingRelationalQuerier{},
		QueryResponseByteLimit: 65_536,
		Logger:                 logger,
	}
}

//nolint:gocyclo // This contract test verifies schema, annotations, and required fields together.
func TestQueryToolHasExactInputSchemaAndAnnotations(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	server, session := newQueryTestSession(t, newMCPTestDependencies(logger))
	defer server.Close()

	result, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}

	var queryTool *mcpsdk.Tool

	for _, tool := range result.Tools {
		if tool.Name == relationalQueryOperation {
			queryTool = tool
			break
		}
	}

	if queryTool == nil {
		t.Fatal("query tool is not registered")
		return
	}

	if queryTool.Annotations == nil || !queryTool.Annotations.ReadOnlyHint ||
		queryTool.Annotations.IdempotentHint || queryTool.Annotations.DestructiveHint != nil ||
		queryTool.Annotations.OpenWorldHint == nil || *queryTool.Annotations.OpenWorldHint {
		t.Fatalf("query annotations = %#v, want read-only/non-idempotent/non-destructive/closed-world", queryTool.Annotations)
	}

	encoded, err := json.Marshal(queryTool.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}

	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatalf("unmarshal input schema: %v", err)
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want object", schema["properties"])
	}

	gotNames := make([]string, 0, len(properties))
	for name := range properties {
		gotNames = append(gotNames, name)
	}

	sort.Strings(gotNames)

	if wantNames := []string{"kind", "query", "source_id"}; !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("input properties = %v, want %v", gotNames, wantNames)
	}

	if !hasRequired(schema, "kind", "source_id", "query") {
		t.Fatalf("required fields = %#v", schema["required"])
	}

	if additional, exists := schema["additionalProperties"]; !exists || additional != false {
		t.Fatalf("additionalProperties = %#v, want false", additional)
	}

	wantDescriptions := map[string]string{
		"kind":      "configured relational database kind",
		"source_id": "globally unique configured source identifier",
		"query":     "one complete row-producing statement",
	}
	for name, want := range wantDescriptions {
		property, ok := properties[name].(map[string]any)
		if !ok || property["description"] != want {
			t.Fatalf("property %q = %#v, want description %q", name, properties[name], want)
		}
	}
}

func TestQueryToolReturnsExactStructuredAndTextResult(t *testing.T) {
	t.Parallel()

	value := "101"
	querier := &recordingRelationalQuerier{
		result: execution.RelationalQueryResult{
			Kind:     connection.Kind("postgres"),
			SourceID: connection.ID("finance"),
			Columns: []execution.RelationalQueryColumn{
				{Name: "id", DatabaseType: "int8"},
				{Name: "id", DatabaseType: "text"},
			},
			Rows:      [][]*string{{&value, nil}},
			RowCount:  1,
			Truncated: false,
		},
	}
	dependencies := newMCPTestDependencies(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	dependencies.RelationalQuerier = querier

	server, session := newQueryTestSession(t, dependencies)
	defer server.Close()

	result := callRelationalQuery(t, session, "select id, id from finance")
	if result.IsError {
		t.Fatalf("query result is an error: %#v", result)
	}

	if len(result.Content) != 1 {
		t.Fatalf("content count = %d, want one", len(result.Content))
	}

	textContent, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want TextContent", result.Content[0])
	}

	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}

	if string(structured) != textContent.Text {
		t.Fatalf("structured/text mismatch: %s != %s", structured, textContent.Text)
	}

	var got map[string]any
	if err := json.Unmarshal(structured, &got); err != nil {
		t.Fatalf("unmarshal query result: %v", err)
	}

	want := map[string]any{
		"kind":      "postgres",
		"source_id": "finance",
		"columns": []any{
			map[string]any{"name": "id", "database_type": "int8"},
			map[string]any{"name": "id", "database_type": "text"},
		},
		"rows":      []any{[]any{"101", nil}},
		"row_count": float64(1),
		"truncated": false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("query result = %#v, want %#v", got, want)
	}

	if len(querier.requests) != 1 || querier.requests[0].Query != "select id, id from finance" {
		t.Fatalf("query requests = %#v, want original query", querier.requests)
	}
}

func TestQueryToolReturnsStructuredFailureWithoutDatabaseError(t *testing.T) {
	t.Parallel()

	querier := &recordingRelationalQuerier{err: execution.ErrInvalidQuery}
	dependencies := newMCPTestDependencies(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	dependencies.RelationalQuerier = querier

	server, session := newQueryTestSession(t, dependencies)
	defer server.Close()

	result := callRelationalQuery(t, session, "select")
	assertQueryFailure(t, result, execution.Failure{
		Category:  execution.ErrorCategoryInvalidQuery,
		Message:   "The query is invalid or does not return columns.",
		Retryable: false,
	})
}

func TestQueryToolReturnsEveryDatabaseErrorField(t *testing.T) {
	t.Parallel()

	databaseError := &execution.DatabaseError{
		Kind:                connection.Kind("postgres"),
		Code:                "42501",
		ExtendedCode:        "SQLITE_BUSY_SNAPSHOT",
		Severity:            "ERROR",
		SeverityUnlocalized: "ERROR",
		Message:             "permission denied",
		Detail:              "detail",
		Hint:                "hint",
		Position:            11,
		InternalPosition:    12,
		InternalQuery:       "select secret",
		Where:               "PL/pgSQL function",
		SchemaName:          "public",
		TableName:           "accounts",
		ColumnName:          "balance",
		DataTypeName:        "numeric",
		ConstraintName:      "accounts_balance_check",
		File:                "aclchk.c",
		Line:                42,
		Routine:             "aclcheck_error",
	}
	querier := &recordingRelationalQuerier{err: databaseError}
	dependencies := newMCPTestDependencies(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	dependencies.RelationalQuerier = querier

	server, session := newQueryTestSession(t, dependencies)
	defer server.Close()

	result := callRelationalQuery(t, session, "select protected")
	assertQueryFailure(t, result, execution.Failure{
		Category:      execution.ErrorCategoryDatabasePermissionDenied,
		Message:       "permission denied",
		Retryable:     false,
		DatabaseError: databaseError,
	})
}

func TestQueryToolBoundsDatabaseFailures(t *testing.T) {
	t.Parallel()

	const byteLimit = 65_536

	var logs bytes.Buffer

	databaseError := &execution.DatabaseError{
		Kind:    "postgres",
		Code:    "22P02",
		Message: strings.Repeat("\\\"\x00\xff", byteLimit),
		Detail:  strings.Repeat("detail", byteLimit),
		Hint:    strings.Repeat("hint", byteLimit),
	}
	querier := &recordingRelationalQuerier{err: databaseError}
	dependencies := newMCPTestDependencies(slog.New(slog.NewJSONHandler(&logs, nil)))
	dependencies.RelationalQuerier = querier
	dependencies.QueryResponseByteLimit = byteLimit

	server, session := newQueryTestSession(t, dependencies)
	defer server.Close()

	result := callRelationalQuery(t, session, "select invalid")
	failure := assertBoundedQueryFailure(t, result, byteLimit)

	if failure.Category != execution.ErrorCategoryInvalidQuery || failure.DatabaseError == nil ||
		failure.DatabaseError.Code != "22P02" || !failure.DatabaseError.Truncated {
		t.Fatalf("bounded failure = %#v, want original category/code with truncation marker", failure)
	}

	if logs.Len() > byteLimit {
		t.Fatalf("failure log size = %d, want at most %d", logs.Len(), byteLimit)
	}

	logRecord := decodeOneLogRecord(t, logs.Bytes())

	databaseFields, ok := logRecord["database_error"].(map[string]any)
	if !ok || databaseFields["truncated"] != true || databaseFields["original_size_bytes"] == nil {
		t.Fatalf("bounded database error log group = %#v", logRecord["database_error"])
	}
}

func TestQueryToolBoundsEveryLargeDatabaseErrorDetail(t *testing.T) {
	t.Parallel()

	const byteLimit = 65_536

	largeValue := strings.Repeat("\\\"\x00", byteLimit)
	tests := []struct {
		name   string
		assign func(*execution.DatabaseError)
	}{
		{name: "message", assign: func(err *execution.DatabaseError) { err.Message = largeValue }},
		{name: "detail", assign: func(err *execution.DatabaseError) { err.Detail = largeValue }},
		{name: "hint", assign: func(err *execution.DatabaseError) { err.Hint = largeValue }},
		{name: "internal query", assign: func(err *execution.DatabaseError) { err.InternalQuery = largeValue }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			databaseError := &execution.DatabaseError{
				Kind:    "postgres",
				Code:    "22P02",
				Message: "invalid input syntax",
			}
			test.assign(databaseError)
			querier := &recordingRelationalQuerier{err: databaseError}
			dependencies := newMCPTestDependencies(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
			dependencies.RelationalQuerier = querier
			dependencies.QueryResponseByteLimit = byteLimit

			server, session := newQueryTestSession(t, dependencies)
			defer server.Close()

			result := callRelationalQuery(t, session, "select invalid")
			failure := assertBoundedQueryFailure(t, result, byteLimit)

			if failure.Category != execution.ErrorCategoryInvalidQuery || failure.DatabaseError == nil ||
				failure.DatabaseError.Code != "22P02" || !failure.DatabaseError.Truncated {
				t.Fatalf("bounded failure = %#v, want original category/code with truncation marker", failure)
			}
		})
	}
}

func TestQueryToolReplacesOversizedSuccess(t *testing.T) {
	t.Parallel()

	value := strings.Repeat("x", 256)
	querier := &recordingRelationalQuerier{result: execution.RelationalQueryResult{
		Kind:     "postgres",
		SourceID: "finance",
		Columns:  []execution.RelationalQueryColumn{{Name: "payload", DatabaseType: "text"}},
		Rows:     [][]*string{{&value}},
		RowCount: 1,
	}}
	dependencies := newMCPTestDependencies(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	dependencies.RelationalQuerier = querier

	minimumFailureSize, err := relationalQueryFailureWireSize(
		execution.ClassifyRelationalQuery(t.Context(), execution.ErrResultTooLarge),
	)
	if err != nil {
		t.Fatalf("relationalQueryFailureWireSize() error = %v", err)
	}

	dependencies.QueryResponseByteLimit = minimumFailureSize

	server, session := newQueryTestSession(t, dependencies)
	defer server.Close()

	result := callRelationalQuery(t, session, "select payload")
	assertQueryFailure(t, result, execution.Failure{
		Category:  execution.ErrorCategoryResultTooLarge,
		Message:   "The encoded query result is too large.",
		Retryable: false,
	})
}

func TestQueryToolChecksExactCallToolResultBoundary(t *testing.T) {
	t.Parallel()

	value := strings.Repeat("x", 65_536)
	output := execution.RelationalQueryResult{
		Kind:     "postgres",
		SourceID: "finance",
		Columns:  []execution.RelationalQueryColumn{{Name: "payload", DatabaseType: "text"}},
		Rows:     [][]*string{{&value}},
		RowCount: 1,
	}

	_, candidateSize, err := relationalQueryCallToolResult(output)
	if err != nil {
		t.Fatalf("relationalQueryCallToolResult() error = %v", err)
	}

	if candidateSize < 65_536 {
		t.Fatalf("candidate size = %d, want at least 65536", candidateSize)
	}

	querier := &recordingRelationalQuerier{result: output}
	dependencies := newMCPTestDependencies(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	dependencies.RelationalQuerier = querier
	dependencies.QueryResponseByteLimit = candidateSize
	server, session := newQueryTestSession(t, dependencies)

	result := callRelationalQuery(t, session, "select payload")
	if result.IsError {
		t.Fatalf("exact boundary result is an error: %#v", result)
	}

	receivedWireResult := &mcpsdk.CallToolResult{
		Content:           result.Content,
		StructuredContent: result.StructuredContent,
	}

	encodedResult, err := json.Marshal(receivedWireResult)
	if err != nil {
		t.Fatalf("marshal received wire result: %v", err)
	}

	if len(encodedResult) != candidateSize {
		t.Fatalf("received result size = %d, helper size = %d", len(encodedResult), candidateSize)
	}

	server.Close()

	dependencies.QueryResponseByteLimit = candidateSize - 1

	server, session = newQueryTestSession(t, dependencies)
	defer server.Close()

	result = callRelationalQuery(t, session, "select payload")
	assertQueryFailure(t, result, execution.Failure{
		Category:  execution.ErrorCategoryResultTooLarge,
		Message:   "The encoded query result is too large.",
		Retryable: false,
	})
}

func TestQueryToolLogsOneContextualSuccess(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	value := "safe-value"
	querier := &recordingRelationalQuerier{result: execution.RelationalQueryResult{
		Kind:     "postgres",
		SourceID: "finance",
		Rows:     [][]*string{{&value}},
		RowCount: 1,
	}}
	dependencies := newMCPTestDependencies(logger)
	dependencies.RelationalQuerier = querier

	server, session := newQueryTestSession(t, dependencies)
	defer server.Close()

	result := callRelationalQuery(t, session, "select safe_value")
	if result.IsError {
		t.Fatalf("query result is an error: %#v", result)
	}

	logRecord := decodeOneLogRecord(t, logs.Bytes())
	if logRecord["msg"] != "relational query completed" ||
		logRecord["level"] != "INFO" ||
		logRecord["operation"] != relationalQueryOperation ||
		logRecord["query_size_bytes"] != float64(len("select safe_value")) ||
		logRecord["kind"] != "postgres" ||
		logRecord["source_id"] != "finance" ||
		logRecord["row_count"] != float64(1) ||
		logRecord["truncated"] != false {
		t.Fatalf("success log = %#v", logRecord)
	}

	if _, exists := logRecord["query"]; exists || strings.Contains(logs.String(), "select safe_value") {
		t.Fatalf("success log leaked raw SQL: %s", logs.String())
	}
}

func TestQueryToolLogsOneContextualFailure(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	databaseError := &execution.DatabaseError{
		Kind:    "postgres",
		Code:    "40001",
		Message: "serialization failure",
		Line:    19,
	}
	querier := &recordingRelationalQuerier{err: databaseError}
	dependencies := newMCPTestDependencies(logger)
	dependencies.RelationalQuerier = querier

	server, session := newQueryTestSession(t, dependencies)
	defer server.Close()

	result := callRelationalQuery(t, session, "select retryable")
	if !result.IsError {
		t.Fatalf("failure result = %#v, want error", result)
	}

	logRecord := decodeOneLogRecord(t, logs.Bytes())
	if logRecord["msg"] != "relational query failed" ||
		logRecord["level"] != "WARN" ||
		logRecord["query_size_bytes"] != float64(len("select retryable")) ||
		logRecord["category"] != string(execution.ErrorCategoryDatabaseConflict) ||
		logRecord["retryable"] != true {
		t.Fatalf("failure log = %#v", logRecord)
	}

	databaseFields, ok := logRecord["database_error"].(map[string]any)
	if !ok || databaseFields["kind"] != "postgres" || databaseFields["code"] != "40001" ||
		databaseFields["line"] != float64(19) {
		t.Fatalf("database error log group = %#v", logRecord["database_error"])
	}

	if _, exists := logRecord["query"]; exists || strings.Contains(logs.String(), "select retryable") {
		t.Fatalf("failure log leaked raw SQL: %s", logs.String())
	}
}

func TestQueryToolDoesNotLogCellsOrCredentials(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	cell := "query-result-cell-canary"
	querier := &recordingRelationalQuerier{result: execution.RelationalQueryResult{
		Kind:     "postgres",
		SourceID: "finance",
		Rows:     [][]*string{{&cell}},
		RowCount: 1,
	}}
	dependencies := newMCPTestDependencies(logger)
	dependencies.RelationalQuerier = querier

	server, session := newQueryTestSession(t, dependencies)
	defer server.Close()

	query := "select protected /* raw-query-canary */"

	result := callRelationalQuery(t, session, query)
	if result.IsError {
		t.Fatalf("query result is an error: %#v", result)
	}

	encodedLogs := logs.String()
	if strings.Contains(encodedLogs, cell) || strings.Contains(encodedLogs, query) || strings.Contains(encodedLogs, "postgres://user:password@") {
		t.Fatalf("logs contain result cells or credentials: %s", encodedLogs)
	}
}

func newQueryTestSession(
	t *testing.T,
	dependencies Dependencies,
) (*httptest.Server, *mcpsdk.ClientSession) {
	t.Helper()

	handler, err := New(dependencies)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := httptest.NewServer(handler)
	session := connectTestSession(t, server, serverVersion)

	return server, session
}

func callRelationalQuery(t *testing.T, session *mcpsdk.ClientSession, query string) *mcpsdk.CallToolResult {
	t.Helper()

	result, err := session.CallTool(
		t.Context(),
		&mcpsdk.CallToolParams{
			Name: relationalQueryOperation,
			Arguments: map[string]any{
				"kind":      "postgres",
				"source_id": "finance",
				"query":     query,
			},
		},
	)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}

	return result
}

func assertBoundedQueryFailure(
	t *testing.T,
	result *mcpsdk.CallToolResult,
	byteLimit int,
) execution.Failure {
	t.Helper()

	if result == nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("failure result = %#v, want one tool error", result)
	}

	wireResult := &mcpsdk.CallToolResult{Content: result.Content, IsError: result.IsError}

	encoded, err := json.Marshal(wireResult)
	if err != nil {
		t.Fatalf("marshal failure result: %v", err)
	}

	if len(encoded) > byteLimit {
		t.Fatalf("encoded failure size = %d, want at most %d", len(encoded), byteLimit)
	}

	textContent, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("failure content type = %T, want TextContent", result.Content[0])
	}

	var failure execution.Failure
	if err := json.Unmarshal([]byte(textContent.Text), &failure); err != nil {
		t.Fatalf("unmarshal bounded failure: %v", err)
	}

	wireSize, err := relationalQueryFailureWireSize(failure)
	if err != nil {
		t.Fatalf("relationalQueryFailureWireSize() error = %v", err)
	}

	if wireSize != len(encoded) {
		t.Fatalf("failure wire size = %d, received size = %d", wireSize, len(encoded))
	}

	return failure
}

func assertQueryFailure(t *testing.T, result *mcpsdk.CallToolResult, want execution.Failure) {
	t.Helper()

	if result == nil || !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("failure result = %#v, want one unstructured tool error", result)
	}

	textContent, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("failure content type = %T, want TextContent", result.Content[0])
	}

	var got execution.Failure
	if err := json.Unmarshal([]byte(textContent.Text), &got); err != nil {
		t.Fatalf("unmarshal failure = %q: %v", textContent.Text, err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failure = %#v, want %#v", got, want)
	}
}

func decodeOneLogRecord(t *testing.T, data []byte) map[string]any {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("logs = %q, want one record", data)
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("unmarshal log = %q: %v", lines[0], err)
	}

	return record
}

type recordingRelationalQuerier struct {
	requests []execution.RelationalQueryRequest
	result   execution.RelationalQueryResult
	err      error
}

func (q *recordingRelationalQuerier) QueryRelationalDatabase(
	_ context.Context,
	request execution.RelationalQueryRequest,
) (execution.RelationalQueryResult, error) {
	q.requests = append(q.requests, request)

	return q.result, q.err
}

var _ RelationalQuerier = (*recordingRelationalQuerier)(nil)
