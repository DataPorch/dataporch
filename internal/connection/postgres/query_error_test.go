package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestProjectRelationalQueryErrorPreservesPostgresFields(t *testing.T) {
	t.Parallel()

	pgError := &pgconn.PgError{
		Severity:            "ERROR",
		SeverityUnlocalized: "ERROR",
		Code:                "42703",
		Message:             "column does not exist",
		Detail:              "detail",
		Hint:                "hint",
		Position:            8,
		InternalPosition:    3,
		InternalQuery:       "internal query",
		Where:               "where",
		SchemaName:          "analytics",
		TableName:           "invoices",
		ColumnName:          "total",
		DataTypeName:        "numeric",
		ConstraintName:      "invoices_total_check",
		File:                "parse_relation.c",
		Line:                3722,
		Routine:             "errorMissingColumn",
	}

	projected := projectRelationalQueryError(fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", pgError)))
	var databaseError *execution.DatabaseError
	if !errors.As(projected, &databaseError) {
		t.Fatalf("projected error = %v, want DatabaseError", projected)
	}

	want := &execution.DatabaseError{
		Kind:                Kind,
		Code:                pgError.Code,
		Severity:            pgError.Severity,
		SeverityUnlocalized: pgError.SeverityUnlocalized,
		Message:             pgError.Message,
		Detail:              pgError.Detail,
		Hint:                pgError.Hint,
		Position:            pgError.Position,
		InternalPosition:    pgError.InternalPosition,
		InternalQuery:       pgError.InternalQuery,
		Where:               pgError.Where,
		SchemaName:          pgError.SchemaName,
		TableName:           pgError.TableName,
		ColumnName:          pgError.ColumnName,
		DataTypeName:        pgError.DataTypeName,
		ConstraintName:      pgError.ConstraintName,
		File:                pgError.File,
		Line:                pgError.Line,
		Routine:             pgError.Routine,
	}
	if !reflect.DeepEqual(databaseError, want) {
		t.Fatalf("database error = %#v, want %#v", databaseError, want)
	}
}

func TestProjectRelationalQueryErrorHidesPrivateCauses(t *testing.T) {
	t.Parallel()

	canary := errors.New("driver credential=secret-canary")
	projected := projectRelationalQueryError(canary)
	if !errors.Is(projected, execution.ErrInternal) {
		t.Fatalf("projected error = %v, want ErrInternal", projected)
	}
	if projected.Error() != execution.ErrInternal.Error() {
		t.Fatalf("projected error text = %q, want %q", projected.Error(), execution.ErrInternal.Error())
	}
	if containsAny(projected.Error(), "secret-canary", "credential") {
		t.Fatalf("projected error leaked private detail: %v", projected)
	}
}

func TestProjectRelationalQueryErrorClassifiesRetrySafeDriverErrors(t *testing.T) {
	t.Parallel()

	projected := projectRelationalQueryError(retrySafeQueryError{})
	if !errors.Is(projected, execution.ErrDatabaseUnavailable) {
		t.Fatalf("projected error = %v, want ErrDatabaseUnavailable", projected)
	}
}

func TestProjectRelationalQueryErrorClassifiesLifecycleErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "open timeout", err: ErrOpenTimeout, want: execution.ErrDatabaseUnavailable},
		{name: "runtime closed", err: ErrRuntimeClosed, want: execution.ErrDatabaseUnavailable},
		{name: "invalidated", err: errOpenInvalidated, want: execution.ErrDatabaseUnavailable},
		{name: "not found", err: connection.ErrDatabaseNotFound, want: execution.ErrDatabaseUnavailable},
		{name: "context canceled", err: context.Canceled, want: execution.ErrCancelled},
		{name: "context deadline", err: context.DeadlineExceeded, want: execution.ErrQueryTimeout},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if err := projectRelationalQueryError(test.err); !errors.Is(err, test.want) {
				t.Fatalf("projected error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestOpenerSharesRawAttemptAcrossProjections(t *testing.T) {
	t.Parallel()

	pool := newOpenerTestPool()
	pool.pingRelease = make(chan struct{})
	pool.pingWait = true
	pool.pingErr = &pgconn.PgError{Code: "28P01", Message: "password authentication failed"}
	factory := &openerTestFactory{results: []openerPoolResult{{pool: pool}}}
	opener := newTestOpener(t, &openerTestPreparer{definition: resolvedPostgresDefinition()}, factory)

	queryResult := make(chan openerResult, 1)
	go func() {
		client, err := opener.OpenQuery(context.Background(), "finance")
		queryResult <- openerResult{client: client, err: err}
	}()
	waitForSignal(t, pool.pingStarted)

	discoveryContext := newWaiterContext()
	discoveryResult := make(chan openerResult, 1)
	go func() {
		client, err := opener.Open(discoveryContext, "finance")
		discoveryResult <- openerResult{client: client, err: err}
	}()
	<-discoveryContext.observed

	if got := factory.callCount(); got != 1 {
		t.Fatalf("pool factory calls = %d, want one shared attempt", got)
	}

	close(pool.pingRelease)
	discovery := receiveResult(t, discoveryResult)
	query := receiveResult(t, queryResult)
	if !errors.Is(discovery.err, connection.ErrDatabaseUnavailable) {
		t.Fatalf("Open() error = %v, want unavailable", discovery.err)
	}

	var leakedDiscoveryError *execution.DatabaseError
	if errors.As(discovery.err, &leakedDiscoveryError) {
		t.Fatalf("Open() exposed query database error: %#v", leakedDiscoveryError)
	}

	var queryDatabaseError *execution.DatabaseError
	if !errors.As(query.err, &queryDatabaseError) || queryDatabaseError.Code != "28P01" {
		t.Fatalf("OpenQuery() result = %#v, discovery result = %#v, want projected 28P01", query, discovery)
	}
}

func TestOpenerQueryWaiterCancellationDoesNotCancelSharedAttempt(t *testing.T) {
	t.Parallel()

	pool := newOpenerTestPool()
	pool.pingRelease = make(chan struct{})
	pool.pingWait = true
	factory := &openerTestFactory{results: []openerPoolResult{{pool: pool}}}
	opener := newTestOpener(t, &openerTestPreparer{definition: resolvedPostgresDefinition()}, factory)

	discoveryResult := make(chan openerResult, 1)
	go func() {
		client, err := opener.Open(context.Background(), "finance")
		discoveryResult <- openerResult{client: client, err: err}
	}()
	waitForSignal(t, pool.pingStarted)

	queryContext, cancel := context.WithCancel(context.Background())
	queryResult := make(chan openerResult, 1)
	go func() {
		client, err := opener.OpenQuery(queryContext, "finance")
		queryResult <- openerResult{client: client, err: err}
	}()
	cancel()

	query := receiveResult(t, queryResult)
	if !errors.Is(query.err, execution.ErrCancelled) {
		t.Fatalf("canceled OpenQuery() error = %v, want ErrCancelled", query.err)
	}

	close(pool.pingRelease)
	discovery := receiveResult(t, discoveryResult)
	if discovery.err != nil || discovery.client == nil {
		t.Fatalf("uncanceled Open() result = %#v, want success", discovery)
	}
	if got := factory.callCount(); got != 1 {
		t.Fatalf("pool factory calls = %d, want one shared attempt", got)
	}
}

type retrySafeQueryError struct{}

func (retrySafeQueryError) Error() string { return "retry-safe driver error" }

func (retrySafeQueryError) SafeToRetry() bool { return true }

type waiterContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func newWaiterContext() *waiterContext {
	return &waiterContext{
		Context:  context.Background(),
		observed: make(chan struct{}),
	}
}

func (c *waiterContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return nil
}
