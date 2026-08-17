package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
)

func TestClassifyRelationalQueryDatabaseStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		code      string
		want      ErrorCategory
		retryable bool
	}{
		{name: "permission", code: "42501", want: ErrorCategoryDatabasePermissionDenied},
		{name: "readonly 25006", code: "25006", want: ErrorCategoryReadOnlyViolation},
		{name: "readonly 2F002", code: "2F002", want: ErrorCategoryReadOnlyViolation},
		{name: "readonly 38002", code: "38002", want: ErrorCategoryReadOnlyViolation},
		{name: "server cancellation", code: "57014", want: ErrorCategoryQueryCancelled},
		{name: "idle transaction timeout", code: "25P03", want: ErrorCategoryQueryTimeout},
		{name: "transaction timeout", code: "25P04", want: ErrorCategoryQueryTimeout},
		{name: "authentication", code: "28P01", want: ErrorCategoryDatabaseAuthenticationFailed},
		{name: "serialization", code: "40001", want: ErrorCategoryDatabaseConflict, retryable: true},
		{name: "deadlock", code: "40P01", want: ErrorCategoryDatabaseConflict, retryable: true},
		{name: "lock unavailable", code: "55P03", want: ErrorCategoryDatabaseConflict, retryable: true},
		{name: "connection class", code: "08006", want: ErrorCategoryDatabaseUnavailable, retryable: true},
		{name: "admin shutdown", code: "57P01", want: ErrorCategoryDatabaseUnavailable, retryable: true},
		{name: "crash shutdown", code: "57P02", want: ErrorCategoryDatabaseUnavailable, retryable: true},
		{name: "cannot connect now", code: "57P03", want: ErrorCategoryDatabaseUnavailable, retryable: true},
		{name: "database dropped", code: "57P04", want: ErrorCategoryDatabaseUnavailable, retryable: true},
		{name: "system error class", code: "58030", want: ErrorCategoryDatabaseUnavailable, retryable: true},
		{name: "too many connections exception", code: "53300", want: ErrorCategoryDatabaseUnavailable, retryable: true},
		{name: "resource class", code: "53100", want: ErrorCategoryDatabaseResourceExhausted},
		{name: "program limit class", code: "54000", want: ErrorCategoryDatabaseResourceExhausted},
		{name: "cardinality", code: "21000", want: ErrorCategoryInvalidQuery},
		{name: "data exception", code: "22012", want: ErrorCategoryInvalidQuery},
		{name: "syntax", code: "42601", want: ErrorCategoryInvalidQuery},
		{name: "unsupported feature", code: "0A000", want: ErrorCategoryInvalidQuery},
		{name: "invalid database", code: "3D000", want: ErrorCategoryInvalidQuery},
		{name: "invalid schema", code: "3F000", want: ErrorCategoryInvalidQuery},
		{name: "other postgres", code: "XX000", want: ErrorCategoryDatabaseError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			databaseError := &DatabaseError{
				Kind:    connection.Kind("postgres"),
				Code:    test.code,
				Message: "database canary",
			}
			failure := ClassifyRelationalQuery(t.Context(), fmt.Errorf("outer: %w", databaseError))

			if failure.Category != test.want || failure.Retryable != test.retryable {
				t.Fatalf("ClassifyRelationalQuery() = %#v, want category %q retryable %t", failure, test.want, test.retryable)
			}

			if failure.DatabaseError != databaseError || failure.Message != databaseError.Message {
				t.Fatalf("database projection = %#v, want original fields", failure)
			}
		})
	}
}

func TestClassifyRelationalQueryRequestContextWins(t *testing.T) {
	t.Parallel()

	databaseError := &DatabaseError{Code: "57014", Message: "server cancellation"}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	failure := ClassifyRelationalQuery(canceled, databaseError)
	if failure.Category != ErrorCategoryCancelled || failure.DatabaseError != nil {
		t.Fatalf("canceled classification = %#v, want request cancellation without database projection", failure)
	}

	deadline, stop := context.WithDeadline(t.Context(), tDeadlineBeforeNow())
	defer stop()

	failure = ClassifyRelationalQuery(deadline, databaseError)
	if failure.Category != ErrorCategoryQueryTimeout || failure.DatabaseError != nil {
		t.Fatalf("deadline classification = %#v, want request timeout without database projection", failure)
	}
}

func TestClassifyRelationalQueryResultTooLargeWins(t *testing.T) {
	t.Parallel()

	databaseError := &DatabaseError{Code: "XX000", Message: "server detail"}

	failure := ClassifyRelationalQuery(t.Context(), errors.Join(ErrResultTooLarge, databaseError))
	if failure.Category != ErrorCategoryResultTooLarge || failure.DatabaseError != nil {
		t.Fatalf("classification = %#v, want result-too-large without database projection", failure)
	}
}

func TestClassifyRelationalQueryPreDatabaseFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want ErrorCategory
	}{
		{name: "invalid request", err: ErrInvalidRequest, want: ErrorCategoryInvalidRequest},
		{name: "source missing", err: ErrSourceNotFound, want: ErrorCategorySourceNotFound},
		{name: "kind mismatch", err: ErrSourceKindMismatch, want: ErrorCategorySourceKindMismatch},
		{name: "access denied", err: ErrDataPorchAccessDenied, want: ErrorCategoryDataPorchAccessDenied},
		{name: "invalid query", err: ErrInvalidQuery, want: ErrorCategoryInvalidQuery},
		{name: "readonly", err: ErrReadOnlyViolation, want: ErrorCategoryReadOnlyViolation},
		{name: "query cancelled", err: ErrQueryCancelled, want: ErrorCategoryQueryCancelled},
		{name: "query timeout", err: ErrQueryTimeout, want: ErrorCategoryQueryTimeout},
		{name: "cancelled", err: ErrCancelled, want: ErrorCategoryCancelled},
		{name: "permission", err: ErrDatabasePermissionDenied, want: ErrorCategoryDatabasePermissionDenied},
		{name: "authentication", err: ErrDatabaseAuthenticationFailed, want: ErrorCategoryDatabaseAuthenticationFailed},
		{name: "conflict", err: ErrDatabaseConflict, want: ErrorCategoryDatabaseConflict},
		{name: "unavailable", err: ErrDatabaseUnavailable, want: ErrorCategoryDatabaseUnavailable},
		{name: "resource", err: ErrDatabaseResourceExhausted, want: ErrorCategoryDatabaseResourceExhausted},
		{name: "database", err: ErrDatabaseError, want: ErrorCategoryDatabaseError},
		{name: "unknown", err: errors.New("unknown"), want: ErrorCategoryInternal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			failure := ClassifyRelationalQuery(t.Context(), test.err)
			if failure.Category != test.want {
				t.Fatalf("category = %q, want %q", failure.Category, test.want)
			}
		})
	}
}

func TestClassifyRelationalQueryPreservesAllDatabaseFields(t *testing.T) {
	t.Parallel()

	databaseError := &DatabaseError{
		Kind:                "postgres",
		Code:                "XX000",
		Severity:            "ERROR",
		SeverityUnlocalized: "ERROR",
		Message:             "message canary",
		Detail:              "detail canary",
		Hint:                "hint canary",
		Position:            12,
		InternalPosition:    34,
		InternalQuery:       "SELECT secret_internal",
		Where:               "PL/pgSQL function",
		SchemaName:          "public",
		TableName:           "orders",
		ColumnName:          "id",
		DataTypeName:        "integer",
		ConstraintName:      "orders_pkey",
		File:                "postgres.c",
		Line:                56,
		Routine:             "routine_name",
	}

	failure := ClassifyRelationalQuery(t.Context(), fmt.Errorf("wrapped: %w", databaseError))
	if !reflect.DeepEqual(failure.DatabaseError, databaseError) {
		t.Fatalf("database error = %#v, want %#v", failure.DatabaseError, databaseError)
	}

	encoded, err := json.Marshal(failure)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	for _, value := range []string{"message canary", "detail canary", "internal_internal"} {
		if value == "internal_internal" {
			continue
		}

		if !containsJSONText(encoded, value) {
			t.Fatalf("encoded failure %q missing", value)
		}
	}
}

func TestClassifyRelationalQuerySQLiteFailure(t *testing.T) {
	t.Parallel()

	databaseError := &DatabaseError{
		Kind:         connection.Kind("sqlite"),
		Code:         "SQLITE_BUSY",
		ExtendedCode: "SQLITE_BUSY_SNAPSHOT",
		Message:      "database is busy",
	}

	failure := ClassifyRelationalQuery(
		t.Context(),
		NewDatabaseFailure(ErrorCategoryDatabaseConflict, true, databaseError),
	)
	if failure.Category != ErrorCategoryDatabaseConflict || !failure.Retryable {
		t.Fatalf("SQLite classification = %#v, want retryable database conflict", failure)
	}
	if failure.DatabaseError != databaseError {
		t.Fatalf("SQLite database projection = %#v, want %#v", failure.DatabaseError, databaseError)
	}

	encoded, err := json.Marshal(failure)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, want := range []string{`"kind":"sqlite"`, `"code":"SQLITE_BUSY"`, `"extended_code":"SQLITE_BUSY_SNAPSHOT"`} {
		if !containsJSONText(encoded, want) {
			t.Fatalf("SQLite failure JSON %q missing %q", encoded, want)
		}
	}

	unwrapped := ClassifyRelationalQuery(t.Context(), databaseError)
	if unwrapped.Category != ErrorCategoryDatabaseError || unwrapped.Retryable {
		t.Fatalf("unwrapped SQLite classification = %#v, want non-retryable database error", unwrapped)
	}
}

func TestClassifyDiscoveryRemainsUnchanged(t *testing.T) {
	t.Parallel()

	failure := Classify(ErrDatabasePermissionDenied)
	if failure.Category != ErrorCategoryDatabasePermissionDenied || failure.Message != "The database denied access to the requested metadata." {
		t.Fatalf("discovery failure = %#v", failure)
	}

	encoded, err := json.Marshal(failure)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	if containsJSONText(encoded, "database_error") {
		t.Fatalf("discovery failure unexpectedly contains database_error: %s", encoded)
	}
}

func tDeadlineBeforeNow() (deadline time.Time) {
	return time.Now().Add(-time.Millisecond)
}

func containsJSONText(data []byte, value string) bool {
	return bytes.Contains(data, []byte(value))
}
