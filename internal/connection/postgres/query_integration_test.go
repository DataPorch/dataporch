//go:build integration

package postgres

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
)

//nolint:funlen,gocyclo,paralleltest,wsl_v5 // The acceptance flow intentionally sequences one fixture-backed executor through dependent cleanup cases.
func TestQueryExecutorPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("DATAPORCH_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DATAPORCH_TEST_POSTGRES_DSN is not set")
	}

	t.Run("row-producing statements", func(t *testing.T) {
		executor, definition := newIntegrationQueryExecutor(t, integrationQueryOptions())

		tests := []struct {
			name        string
			query       string
			wantColumns int
			wantRows    int
		}{
			{name: "select", query: "SELECT 1::bigint AS id, NULL::text AS note", wantColumns: 2, wantRows: 1},
			{name: "values", query: "VALUES (1::integer, 'one'::text), (2, 'two')", wantColumns: 2, wantRows: 2},
			{name: "show", query: "SHOW server_version", wantColumns: 1, wantRows: 1},
			{name: "explain", query: "EXPLAIN SELECT 1", wantColumns: 1, wantRows: 1},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				result, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
					Source: definition,
					Query:  test.query,
				})
				if err != nil {
					t.Fatalf("Query() error = %v", err)
				}

				if result.Kind != Kind || result.SourceID != definition.ID {
					t.Fatalf("result source = %q/%q, want %q/%q", result.Kind, result.SourceID, Kind, definition.ID)
				}
				if len(result.Columns) != test.wantColumns || len(result.Rows) != test.wantRows ||
					result.RowCount != len(result.Rows) || result.Rows == nil || result.Truncated {
					t.Fatalf("result = %#v, want %d columns and %d complete rows", result, test.wantColumns, test.wantRows)
				}
			})
		}

		t.Run("preserves bigint and null", func(t *testing.T) {
			result, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
				Source: definition,
				Query:  "SELECT 1::bigint AS id, NULL::text AS note",
			})
			if err != nil {
				t.Fatalf("Query() error = %v", err)
			}
			if result.Columns[0].DatabaseType != "int8" || result.Rows[0][0] == nil ||
				*result.Rows[0][0] != "1" || result.Rows[0][1] != nil {
				t.Fatalf("result = %#v, want int8 text 1 and null", result)
			}
		})
	})

	t.Run("rejects unsafe or non-row statements", func(t *testing.T) {
		executor, definition := newIntegrationQueryExecutor(t, integrationQueryOptions())

		tests := []struct {
			name     string
			query    string
			wantKind execution.ErrorCategory
		}{
			{name: "read only", query: "CREATE TABLE dataporch_read_only_probe (id integer)", wantKind: execution.ErrorCategoryReadOnlyViolation},
			{name: "multiple statements", query: "SELECT 1; SELECT 2", wantKind: execution.ErrorCategoryInvalidQuery},
			{name: "rowless", query: "SET LOCAL application_name = 'dataporch-rowless-probe'", wantKind: execution.ErrorCategoryInvalidQuery},
			{name: "undefined column", query: "SELECT dataporch_undefined_column", wantKind: execution.ErrorCategoryInvalidQuery},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				result, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
					Source: definition,
					Query:  test.query,
				})
				if err == nil {
					t.Fatalf("Query() result = %#v, want error", result)
				}

				failure := execution.ClassifyRelationalQuery(t.Context(), err)
				if failure.Category != test.wantKind {
					t.Fatalf("failure = %#v, want category %q", failure, test.wantKind)
				}
			})
		}
	})

	t.Run("truncation", func(t *testing.T) {
		executor, definition := newIntegrationQueryExecutor(t, QueryOptions{
			Timeout:           20 * time.Second,
			ResponseByteLimit: 65_536,
			TruncationEnabled: true,
			RowLimit:          3,
		})

		truncated, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
			Source: definition,
			Query:  "SELECT generate_series(1, 4)",
		})
		if err != nil {
			t.Fatalf("truncated Query() error = %v", err)
		}
		if len(truncated.Rows) != 3 || truncated.RowCount != 3 || !truncated.Truncated {
			t.Fatalf("truncated result = %#v, want three rows and truncated=true", truncated)
		}

		complete, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
			Source: definition,
			Query:  "SELECT generate_series(1, 3)",
		})
		if err != nil {
			t.Fatalf("complete Query() error = %v", err)
		}
		if len(complete.Rows) != 3 || complete.RowCount != 3 || complete.Truncated {
			t.Fatalf("complete result = %#v, want three rows and truncated=false", complete)
		}
	})

	t.Run("byte limit without row truncation", func(t *testing.T) {
		executor, definition := newIntegrationQueryExecutor(t, QueryOptions{
			Timeout:           20 * time.Second,
			ResponseByteLimit: 65_536,
			TruncationEnabled: false,
		})

		result, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
			Source: definition,
			Query:  "SELECT repeat('x', 100)",
		})
		if err != nil {
			t.Fatalf("small Query() error = %v", err)
		}
		if result.RowCount != 1 || result.Truncated {
			t.Fatalf("small result = %#v, want one complete row", result)
		}

		result, err = executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
			Source: definition,
			Query:  "SELECT repeat('x', 70000)",
		})
		if err == nil {
			t.Fatalf("large Query() result = %#v, want result-too-large error", result)
		}
		failure := execution.ClassifyRelationalQuery(t.Context(), err)
		if failure.Category != execution.ErrorCategoryResultTooLarge ||
			!reflect.DeepEqual(result, execution.RelationalQueryResult{}) {
			t.Fatalf("large result/failure = %#v/%#v, want zero result and result_too_large", result, failure)
		}
	})

	t.Run("timeout and cancellation", func(t *testing.T) {
		executor, definition := newIntegrationQueryExecutor(t, QueryOptions{
			Timeout:           time.Second,
			ResponseByteLimit: 65_536,
			TruncationEnabled: true,
			RowLimit:          100,
		})

		_, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
			Source: definition,
			Query:  "SELECT pg_sleep(2)",
		})
		if err == nil || execution.ClassifyRelationalQuery(t.Context(), err).Category != execution.ErrorCategoryQueryTimeout {
			t.Fatalf("statement timeout error = %v, want query_timeout", err)
		}

		_, err = executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
			Source: definition,
			Query:  "SELECT value, pg_sleep(0.6) FROM generate_series(1, 4) AS value",
		})
		if err == nil || execution.ClassifyRelationalQuery(t.Context(), err).Category != execution.ErrorCategoryQueryTimeout {
			t.Fatalf("iteration timeout error = %v, want query_timeout", err)
		}

		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = executor.Query(canceled, execution.RelationalQueryExecutionRequest{
			Source: definition,
			Query:  "SELECT 1",
		})
		if err == nil || execution.ClassifyRelationalQuery(canceled, err).Category != execution.ErrorCategoryCancelled {
			t.Fatalf("canceled error = %v, want cancelled", err)
		}
	})

	t.Run("cleanup on reused backend", func(t *testing.T) {
		executor, definition := newIntegrationQueryExecutor(t, integrationQueryOptions())

		_, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
			Source: definition,
			Query:  "PREPARE dataporch_cleanup_probe AS SELECT 1",
		})
		if err == nil || execution.ClassifyRelationalQuery(t.Context(), err).Category != execution.ErrorCategoryInvalidQuery {
			t.Fatalf("PREPARE error = %v, want invalid_query", err)
		}

		prepared, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
			Source: definition,
			Query:  "SELECT count(*) FROM pg_prepared_statements WHERE name = 'dataporch_cleanup_probe'",
		})
		if err != nil {
			t.Fatalf("prepared statement cleanup query error = %v", err)
		}
		if prepared.Rows[0][0] == nil || *prepared.Rows[0][0] != "0" {
			t.Fatalf("prepared statement count = %#v, want 0", prepared.Rows)
		}

		locked, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
			Source: definition,
			Query:  "SELECT pg_backend_pid(), pg_advisory_lock(724611289)",
		})
		if err != nil {
			t.Fatalf("advisory lock query error = %v", err)
		}
		second, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
			Source: definition,
			Query: "SELECT pg_backend_pid(), (SELECT count(*) FROM pg_locks " +
				"WHERE locktype = 'advisory' AND pid = pg_backend_pid())",
		})
		if err != nil {
			t.Fatalf("advisory lock cleanup query error = %v", err)
		}

		if locked.Rows[0][0] == nil || second.Rows[0][0] == nil {
			t.Fatal("backend PID results contain nil")
		}
		firstPID, err := strconv.ParseInt(*locked.Rows[0][0], 10, 64)
		if err != nil {
			t.Fatalf("first backend PID = %q: %v", *locked.Rows[0][0], err)
		}
		secondPID, err := strconv.ParseInt(*second.Rows[0][0], 10, 64)
		if err != nil {
			t.Fatalf("second backend PID = %q: %v", *second.Rows[0][0], err)
		}
		if firstPID == secondPID && (second.Rows[0][1] == nil || *second.Rows[0][1] != "0") {
			t.Fatalf("advisory locks on reused backend = %#v, want 0", second.Rows)
		}
	})
}

func newIntegrationQueryExecutor(
	t *testing.T,
	options QueryOptions,
) (*QueryExecutor, connection.Definition) {
	t.Helper()

	dsn := os.Getenv("DATAPORCH_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DATAPORCH_TEST_POSTGRES_DSN is not set")
	}

	definition, password := integrationDefinition(t, dsn)
	resolver := &integrationSecretResolver{password: password}

	t.Cleanup(func() {
		resolver.clearReturned()
		clear(password)
	})

	opener := newIntegrationOpener(t, resolver, definition)
	t.Cleanup(func() {
		if err := opener.Close(context.Background()); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	executor, err := NewQueryExecutor(opener, options)
	if err != nil {
		t.Fatalf("NewQueryExecutor() error = %v", err)
	}

	return executor, definition
}

func integrationQueryOptions() QueryOptions {
	return QueryOptions{
		Timeout:           20 * time.Second,
		ResponseByteLimit: 10_485_760,
		TruncationEnabled: true,
		RowLimit:          1000,
	}
}
