package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/adamraziv/dataporch/internal/execution"
	gomysql "github.com/go-sql-driver/mysql"
)

func TestProjectMySQLError(t *testing.T) {
	t.Parallel()

	mysqlErr := &gomysql.MySQLError{
		Number:   1142,
		SQLState: [5]byte{'4', '2', '0', '0', '0'},
		Message:  "SELECT command denied",
	}

	projected := projectMySQLError(mysqlErr)
	if projected.Kind != Kind || projected.Code != "1142" || projected.ExtendedCode != "42000" || projected.Message != mysqlErr.Message {
		t.Fatalf("projected error = %#v", projected)
	}
}

func TestMySQLErrorCategory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		number    uint16
		state     string
		category  execution.ErrorCategory
		retryable bool
	}{
		{name: "authentication", number: 1045, category: execution.ErrorCategoryDatabaseAuthenticationFailed},
		{name: "access denied", number: 1044, category: execution.ErrorCategoryDatabasePermissionDenied},
		{name: "select denied", number: 1142, category: execution.ErrorCategoryDatabasePermissionDenied},
		{name: "column denied", number: 1143, category: execution.ErrorCategoryDatabasePermissionDenied},
		{name: "set user", number: 1227, category: execution.ErrorCategoryDatabasePermissionDenied},
		{name: "read only number", number: 1792, category: execution.ErrorCategoryReadOnlyViolation},
		{name: "read only state", state: "25006", category: execution.ErrorCategoryReadOnlyViolation},
		{name: "cancelled", number: 1317, category: execution.ErrorCategoryQueryCancelled},
		{name: "timeout", number: 3024, category: execution.ErrorCategoryQueryTimeout},
		{name: "deadlock", number: 1213, category: execution.ErrorCategoryDatabaseConflict, retryable: true},
		{name: "lock wait", number: 1205, category: execution.ErrorCategoryDatabaseConflict, retryable: true},
		{name: "too many connections", number: 1040, category: execution.ErrorCategoryDatabaseUnavailable, retryable: true},
		{name: "out of memory", number: 1041, category: execution.ErrorCategoryDatabaseResourceExhausted},
		{name: "sort memory", number: 1037, category: execution.ErrorCategoryDatabaseResourceExhausted},
		{name: "disk full", number: 1114, category: execution.ErrorCategoryDatabaseResourceExhausted},
		{name: "connection state", state: "08006", category: execution.ErrorCategoryDatabaseUnavailable, retryable: true},
		{name: "integrity state", state: "22000", category: execution.ErrorCategoryInvalidQuery},
		{name: "syntax state", state: "42000", category: execution.ErrorCategoryInvalidQuery},
		{name: "feature state", state: "0A000", category: execution.ErrorCategoryInvalidQuery},
		{name: "database state", state: "3D000", category: execution.ErrorCategoryInvalidQuery},
		{name: "unknown", number: 9999, category: execution.ErrorCategoryDatabaseError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var state [5]byte
			copy(state[:], test.state)
			projected := projectRelationalQueryError(&gomysql.MySQLError{
				Number:   test.number,
				SQLState: state,
				Message:  "native failure",
			})
			failure := execution.ClassifyRelationalQuery(context.Background(), projected)
			if failure.Category != test.category || failure.Retryable != test.retryable {
				t.Fatalf("failure = %#v, want category=%q retryable=%v", failure, test.category, test.retryable)
			}
		})
	}
}

func TestProjectRelationalQueryError(t *testing.T) {
	t.Parallel()

	if err := projectRelationalQueryError(driver.ErrBadConn); !errors.Is(err, execution.ErrDatabaseUnavailable) {
		t.Fatalf("driver.ErrBadConn projection = %v, want unavailable", err)
	}

	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	if err := projectRelationalQueryError(dialErr); !errors.Is(err, execution.ErrDatabaseUnavailable) {
		t.Fatalf("dial projection = %v, want unavailable", err)
	}
	if err := projectRelationalQueryError(errOpenInvalidated); !errors.Is(err, execution.ErrDatabaseUnavailable) {
		t.Fatalf("invalidation projection = %v, want unavailable", err)
	}

	canary := errors.New("driver credential=secret-canary")
	projected := projectRelationalQueryError(canary)
	if !errors.Is(projected, execution.ErrInternal) || strings.Contains(projected.Error(), "secret-canary") {
		t.Fatalf("generic projection = %v, want safe internal error", projected)
	}
}

func TestProjectRelationalQueryErrorContextPrecedence(t *testing.T) {
	t.Parallel()

	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := projectMySQLQueryError(requestCtx, context.Background(), &gomysql.MySQLError{Number: 1045}); !errors.Is(err, execution.ErrCancelled) {
		t.Fatalf("cancelled request projection = %v, want cancelled", err)
	}

	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadlineCancel()
	if err := projectMySQLQueryError(deadlineCtx, context.Background(), &gomysql.MySQLError{Number: 1045}); !errors.Is(err, execution.ErrQueryTimeout) {
		t.Fatalf("deadline request projection = %v, want timeout", err)
	}

	queryCtx, queryCancel := context.WithCancelCause(context.Background())
	queryCancel(execution.ErrQueryTimeout)
	if err := projectMySQLQueryError(context.Background(), queryCtx, &gomysql.MySQLError{Number: 1045}); !errors.Is(err, execution.ErrQueryTimeout) {
		t.Fatalf("query timeout projection = %v, want timeout", err)
	}
}

func TestMySQLDiagnosticBoundsAndRedacts(t *testing.T) {
	t.Parallel()

	message := strings.Repeat("a", 511) + "😀"
	got := mysqlDiagnostic(message)
	if !utf8.ValidString(got) || len(got) > 512 || got != strings.Repeat("a", 511) {
		t.Fatalf("mysqlDiagnostic() = %q, valid=%t length=%d", got, utf8.ValidString(got), len(got))
	}

	redacted := mysqlDiagnostic("failed at /home/ubuntu/private/mysql.sock")
	if strings.Contains(redacted, "/home/ubuntu/private") {
		t.Fatalf("mysqlDiagnostic() leaked path: %q", redacted)
	}
}

func TestProjectMySQLDatabaseFailureFields(t *testing.T) {
	t.Parallel()

	err := &gomysql.MySQLError{Number: 1045, Message: "access denied"}
	projected := projectRelationalQueryError(fmt.Errorf("outer: %w", err))
	var databaseFailure *execution.DatabaseFailure
	if !errors.As(projected, &databaseFailure) {
		t.Fatalf("projected error = %v, want DatabaseFailure", projected)
	}
	failure := execution.ClassifyRelationalQuery(context.Background(), projected)
	if failure.DatabaseError == nil || failure.DatabaseError.Code != "1045" || failure.DatabaseError.Kind != Kind {
		t.Fatalf("database failure = %#v", failure)
	}
}
