package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/execution"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

func TestSQLiteCodeSymbolsCoverPinnedDriverCodes(t *testing.T) {
	t.Parallel()

	for code, symbol := range primaryCodeSymbols {
		if !strings.HasPrefix(symbol, "SQLITE_") || symbol == "SQLITE_UNKNOWN" || primaryCodeSymbol(code) != symbol {
			t.Errorf("primary code %v symbol = %q", code, symbol)
		}
	}
	for code, symbol := range extendedCodeSymbols {
		if !strings.HasPrefix(symbol, "SQLITE_") || symbol == "SQLITE_UNKNOWN" || extendedCodeSymbol(code) != symbol {
			t.Errorf("extended code %v symbol = %q", code, symbol)
		}
	}
	if primaryCodeSymbol(sqlite3.ErrorCode(255)) != "SQLITE_UNKNOWN" || extendedCodeSymbol(sqlite3.ExtendedErrorCode(0xffff)) != "SQLITE_UNKNOWN" {
		t.Fatal("unknown SQLite codes were not projected to SQLITE_UNKNOWN")
	}
}

func TestProjectSQLiteErrorsByCodeFamily(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		phase    sqliteErrorPhase
		category execution.ErrorCategory
		retry    bool
		code     string
		extended string
	}{
		{name: "auth", err: sqlite3.ErrorCode(sqlite3.AUTH), category: execution.ErrorCategoryDatabasePermissionDenied, code: "SQLITE_AUTH"},
		{name: "perm", err: sqlite3.ErrorCode(sqlite3.PERM), category: execution.ErrorCategoryDatabasePermissionDenied, code: "SQLITE_PERM"},
		{name: "readonly", err: sqlite3.ErrorCode(sqlite3.READONLY), category: execution.ErrorCategoryReadOnlyViolation, code: "SQLITE_READONLY"},
		{name: "busy snapshot", err: sqlite3.ExtendedErrorCode(sqlite3.BUSY_SNAPSHOT), category: execution.ErrorCategoryDatabaseConflict, retry: true, code: "SQLITE_BUSY", extended: "SQLITE_BUSY_SNAPSHOT"},
		{name: "cantopen", err: sqlite3.ErrorCode(sqlite3.CANTOPEN), category: execution.ErrorCategoryDatabaseUnavailable, retry: true, code: "SQLITE_CANTOPEN"},
		{name: "ioerr", err: sqlite3.ExtendedErrorCode(sqlite3.IOERR_READ), category: execution.ErrorCategoryDatabaseUnavailable, retry: true, code: "SQLITE_IOERR", extended: "SQLITE_IOERR_READ"},
		{name: "prepare syntax", err: sqlite3.ErrorCode(sqlite3.ERROR), phase: sqliteErrorPhasePrepare, category: execution.ErrorCategoryInvalidQuery, code: "SQLITE_ERROR"},
		{name: "step error", err: sqlite3.ErrorCode(sqlite3.ERROR), phase: sqliteErrorPhaseStep, category: execution.ErrorCategoryDatabaseError, code: "SQLITE_ERROR"},
		{name: "range", err: sqlite3.ErrorCode(sqlite3.RANGE), phase: sqliteErrorPhasePrepare, category: execution.ErrorCategoryInvalidQuery, code: "SQLITE_RANGE"},
		{name: "mismatch", err: sqlite3.ErrorCode(sqlite3.MISMATCH), phase: sqliteErrorPhasePrepare, category: execution.ErrorCategoryInvalidQuery, code: "SQLITE_MISMATCH"},
		{name: "misuse", err: sqlite3.ErrorCode(sqlite3.MISUSE), phase: sqliteErrorPhasePrepare, category: execution.ErrorCategoryInvalidQuery, code: "SQLITE_MISUSE"},
		{name: "toobig", err: sqlite3.ErrorCode(sqlite3.TOOBIG), category: execution.ErrorCategoryDatabaseResourceExhausted, code: "SQLITE_TOOBIG"},
		{name: "nomem", err: sqlite3.ErrorCode(sqlite3.NOMEM), category: execution.ErrorCategoryDatabaseResourceExhausted, code: "SQLITE_NOMEM"},
		{name: "full", err: sqlite3.ErrorCode(sqlite3.FULL), category: execution.ErrorCategoryDatabaseResourceExhausted, code: "SQLITE_FULL"},
		{name: "notadb", err: sqlite3.ErrorCode(sqlite3.NOTADB), category: execution.ErrorCategoryDatabaseError, code: "SQLITE_NOTADB"},
		{name: "corrupt", err: sqlite3.ErrorCode(sqlite3.CORRUPT), category: execution.ErrorCategoryDatabaseError, code: "SQLITE_CORRUPT"},
		{name: "interrupt", err: sqlite3.ErrorCode(sqlite3.INTERRUPT), category: execution.ErrorCategoryQueryCancelled, code: "SQLITE_INTERRUPT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projected := projectSQLiteError(context.Background(), context.Background(), fmt.Errorf("wrapped: %w", test.err), test.phase)
			failure := databaseFailureFromError(t, projected)
			if failure.Category != test.category || failure.Retryable != test.retry {
				t.Fatalf("failure = %#v, want category=%q retryable=%v", failure, test.category, test.retry)
			}
			if failure.DatabaseError == nil || failure.DatabaseError.Code != test.code || failure.DatabaseError.ExtendedCode != test.extended {
				t.Fatalf("database error = %#v, want code=%q extended=%q", failure.DatabaseError, test.code, test.extended)
			}
		})
	}
}

func TestProjectSQLiteErrorContextPrecedenceAndIdempotence(t *testing.T) {
	t.Parallel()

	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := projectSQLiteError(requestCtx, context.Background(), sqlite3.ErrorCode(sqlite3.AUTH), sqliteErrorPhaseStep); !errors.Is(err, execution.ErrCancelled) {
		t.Fatalf("cancelled request error = %v, want ErrCancelled", err)
	}

	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadlineCancel()
	if err := projectSQLiteError(deadlineCtx, context.Background(), sqlite3.ErrorCode(sqlite3.AUTH), sqliteErrorPhaseStep); !errors.Is(err, execution.ErrQueryTimeout) {
		t.Fatalf("deadline request error = %v, want ErrQueryTimeout", err)
	}

	queryCtx, queryCancel := context.WithCancel(context.Background())
	queryCancel()
	if err := projectSQLiteError(context.Background(), queryCtx, sqlite3.ErrorCode(sqlite3.INTERRUPT), sqliteErrorPhaseStep); !errors.Is(err, execution.ErrQueryCancelled) {
		t.Fatalf("cancelled query error = %v, want ErrQueryCancelled", err)
	}

	projected := projectSQLiteError(context.Background(), context.Background(), sqlite3.ErrorCode(sqlite3.AUTH), sqliteErrorPhaseStep)
	if got := projectSQLiteError(context.Background(), context.Background(), projected, sqliteErrorPhaseStep); got != projected {
		t.Fatal("already projected failure was wrapped again")
	}
}

func TestProjectSQLiteErrorRedactsDiagnosticsAndOpenFailures(t *testing.T) {
	t.Parallel()

	raw := errors.New("sqlite failed for /home/ubuntu/private/database.db while running SELECT secret")
	projected := projectSQLiteError(context.Background(), context.Background(), raw, sqliteErrorPhaseStep)
	if errors.Is(projected, os.ErrPermission) || strings.Contains(projected.Error(), "/home/ubuntu/private") {
		t.Fatalf("projected error leaked private details: %v", projected)
	}
	failure := databaseFailureFromError(t, projected)
	if failure.DatabaseError == nil || strings.Contains(failure.DatabaseError.Message, "/home/ubuntu/private") {
		t.Fatalf("database diagnostic leaked path: %#v", failure.DatabaseError)
	}

	if err := projectSQLiteError(context.Background(), context.Background(), errSQLiteFileUnavailable, sqliteErrorPhaseOpen); !errors.Is(err, execution.ErrDatabaseUnavailable) {
		t.Fatalf("file unavailable error = %v, want ErrDatabaseUnavailable", err)
	}
}

func databaseFailureFromError(t *testing.T, err error) execution.Failure {
	t.Helper()

	failure := execution.ClassifyRelationalQuery(context.Background(), err)
	if failure.DatabaseError == nil {
		t.Fatalf("ClassifyRelationalQuery(%v) = %#v, want database error", err, failure)
	}
	return failure
}
