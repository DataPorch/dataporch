package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
)

type fakeQueryOpener struct {
	calls    int
	recorder *queryCallRecorder
	client   *Client
	err      error
}

func (o *fakeQueryOpener) OpenQuery(context.Context, connection.ID) (*Client, error) {
	o.calls++
	if o.recorder != nil {
		o.recorder.add("OpenQuery")
	}
	return o.client, o.err
}

func TestNewQueryExecutorRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	opener := &fakeQueryOpener{}
	valid := QueryOptions{
		Timeout:           time.Second,
		ResponseByteLimit: 1024,
		TruncationEnabled: true,
		RowLimit:          10,
	}

	tests := []struct {
		name    string
		opener  queryClientOpener
		options QueryOptions
		want    error
	}{
		{name: "nil opener", options: valid, want: errQueryOpenerRequired},
		{name: "non positive timeout", opener: opener, options: QueryOptions{ResponseByteLimit: 1024}, want: errRelationalQueryTimeoutRequired},
		{name: "non positive byte limit", opener: opener, options: QueryOptions{Timeout: time.Second}, want: errQueryByteLimitRequired},
		{name: "truncation without row limit", opener: opener, options: QueryOptions{Timeout: time.Second, ResponseByteLimit: 1024, TruncationEnabled: true}, want: errQueryRowLimitRequired},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewQueryExecutor(test.opener, test.options)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewQueryExecutor() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestQueryExecutorRejectsInvalidRequestsBeforeOpen(t *testing.T) {
	t.Parallel()

	validOptions := QueryOptions{Timeout: time.Second, ResponseByteLimit: 4096}
	validRequest := execution.RelationalQueryExecutionRequest{
		Source: connection.Definition{ID: "finance", Kind: Kind},
		Query:  "SELECT 1",
	}

	tests := []struct {
		name    string
		context func() context.Context
		mutate  func(*execution.RelationalQueryExecutionRequest)
		want    error
	}{
		{name: "nil context", context: func() context.Context { return nil }, mutate: func(*execution.RelationalQueryExecutionRequest) {}, want: execution.ErrCancelled},
		{name: "cancelled context", context: func() context.Context { ctx, cancel := context.WithCancel(t.Context()); cancel(); return ctx }, mutate: func(*execution.RelationalQueryExecutionRequest) {}, want: execution.ErrCancelled},
		{name: "empty source id", context: t.Context, mutate: func(request *execution.RelationalQueryExecutionRequest) { request.Source.ID = "" }, want: execution.ErrInvalidRequest},
		{name: "wrong source kind", context: t.Context, mutate: func(request *execution.RelationalQueryExecutionRequest) { request.Source.Kind = "postgres" }, want: execution.ErrInvalidRequest},
		{name: "blank query", context: t.Context, mutate: func(request *execution.RelationalQueryExecutionRequest) { request.Query = " \t\n" }, want: execution.ErrInvalidRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opener := &fakeQueryOpener{}
			executor, err := NewQueryExecutor(opener, validOptions)
			if err != nil {
				t.Fatalf("NewQueryExecutor() error = %v", err)
			}
			request := validRequest
			test.mutate(&request)
			_, err = executor.Query(test.context(), request)
			if !errors.Is(err, test.want) {
				t.Fatalf("Query() error = %v, want %v", err, test.want)
			}
			if opener.calls != 0 {
				t.Fatalf("OpenQuery() calls = %d, want zero", opener.calls)
			}
		})
	}
}

type queryCallRecorder struct{ calls []string }

func (r *queryCallRecorder) add(call string) { r.calls = append(r.calls, call) }

type recordingQueryPool struct {
	recorder   *queryCallRecorder
	connection queryConnection
	acquireErr error
}

func (*recordingQueryPool) Ping(context.Context) error { return nil }

func (*recordingQueryPool) Query(context.Context, string, ...any) (catalogRows, error) {
	return nil, errors.New("unexpected catalog query")
}

func (*recordingQueryPool) Close() error { return nil }

func (p *recordingQueryPool) Acquire(context.Context) (queryConnection, error) {
	p.recorder.add("Acquire")
	return p.connection, p.acquireErr
}

type recordingQueryConnection struct {
	recorder    *queryCallRecorder
	transaction queryTransaction
	beginErr    error
	destroyErr  error
}

func (c *recordingQueryConnection) BeginTx(_ context.Context, options *sql.TxOptions) (queryTransaction, error) {
	mode := "readwrite"
	if options != nil && options.ReadOnly {
		mode = "readonly"
	}
	c.recorder.add("BeginTx:" + mode)
	return c.transaction, c.beginErr
}

func (c *recordingQueryConnection) Destroy() error {
	c.recorder.add("Destroy")
	return c.destroyErr
}

type recordingQueryTransaction struct {
	recorder    *queryCallRecorder
	rows        queryRows
	queryErr    error
	rollbackErr error
	gotQuery    string
	gotArgs     []any
}

func (transaction *recordingQueryTransaction) QueryContext(
	_ context.Context,
	query string,
	args ...any,
) (queryRows, error) {
	transaction.gotQuery = query
	transaction.gotArgs = append([]any(nil), args...)
	transaction.recorder.add("QueryContext:" + query)
	return transaction.rows, transaction.queryErr
}

func (transaction *recordingQueryTransaction) Rollback() error {
	transaction.recorder.add("Rollback")
	return transaction.rollbackErr
}

type recordingQueryRows struct {
	queryRows
	recorder *queryCallRecorder
}

func (rows *recordingQueryRows) Close() error {
	rows.recorder.add("Rows.Close")
	return rows.queryRows.Close()
}

type queryTestFixture struct {
	recorder    *queryCallRecorder
	probe       *resultProbeRows
	rows        *recordingQueryRows
	transaction *recordingQueryTransaction
	connection  *recordingQueryConnection
	pool        *recordingQueryPool
	opener      *fakeQueryOpener
	executor    *QueryExecutor
}

func newQueryTestFixture(t *testing.T) *queryTestFixture {
	t.Helper()

	recorder := &queryCallRecorder{}
	probe := &resultProbeRows{
		columns: []string{"value"}, databaseTypes: []string{"VARCHAR"},
		values: [][]driver.Value{{"ok"}},
	}
	rows := &recordingQueryRows{queryRows: openResultProbeRows(t, probe), recorder: recorder}
	transaction := &recordingQueryTransaction{recorder: recorder, rows: rows}
	connection := &recordingQueryConnection{recorder: recorder, transaction: transaction}
	pool := &recordingQueryPool{recorder: recorder, connection: connection}
	opener := &fakeQueryOpener{recorder: recorder, client: &Client{pool: pool, database: "finance"}}
	executor, err := NewQueryExecutor(opener, QueryOptions{
		Timeout: time.Second, ResponseByteLimit: 4096,
		TruncationEnabled: true, RowLimit: 10,
	})
	if err != nil {
		t.Fatalf("NewQueryExecutor() error = %v", err)
	}
	return &queryTestFixture{
		recorder: recorder, probe: probe, rows: rows, transaction: transaction,
		connection: connection, pool: pool, opener: opener, executor: executor,
	}
}

func TestQueryExecutorForwardsOpaqueSQLAndCleansUp(t *testing.T) {
	t.Parallel()

	fixture := newQueryTestFixture(t)
	query := "SELECT /* opaque */ value"
	result, err := fixture.executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
		Source: connection.Definition{ID: "finance", Kind: Kind}, Query: query,
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	wantCalls := []string{
		"OpenQuery", "Acquire", "BeginTx:readonly", "QueryContext:" + query,
		"Rows.Close", "Rollback", "Destroy",
	}
	if !reflect.DeepEqual(fixture.recorder.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", fixture.recorder.calls, wantCalls)
	}
	if fixture.transaction.gotQuery != query || len(fixture.transaction.gotArgs) != 0 {
		t.Fatalf("forwarded query=%q args=%#v", fixture.transaction.gotQuery, fixture.transaction.gotArgs)
	}
	if result.RowCount != 1 || len(result.Rows) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestQueryExecutorFailureCleanup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*queryTestFixture)
		wantCalls []string
		wantZero  bool
	}{
		{
			name:      "open",
			configure: func(f *queryTestFixture) { f.opener.err = driver.ErrBadConn; f.opener.client = nil },
			wantCalls: []string{"OpenQuery"}, wantZero: true,
		},
		{
			name:      "acquire",
			configure: func(f *queryTestFixture) { f.pool.acquireErr = driver.ErrBadConn; f.pool.connection = nil },
			wantCalls: []string{"OpenQuery", "Acquire"}, wantZero: true,
		},
		{
			name:      "begin",
			configure: func(f *queryTestFixture) { f.connection.beginErr = driver.ErrBadConn; f.connection.transaction = nil },
			wantCalls: []string{"OpenQuery", "Acquire", "BeginTx:readonly", "Destroy"}, wantZero: true,
		},
		{
			name: "query",
			configure: func(f *queryTestFixture) {
				f.transaction.queryErr = execution.ErrInvalidQuery
				f.transaction.rows = nil
			},
			wantCalls: []string{"OpenQuery", "Acquire", "BeginTx:readonly", "QueryContext:SELECT 1", "Rollback", "Destroy"}, wantZero: true,
		},
		{
			name:      "row terminal",
			configure: func(f *queryTestFixture) { f.probe.terminalErr = execution.ErrInvalidQuery },
			wantCalls: []string{"OpenQuery", "Acquire", "BeginTx:readonly", "QueryContext:SELECT 1", "Rows.Close", "Rollback", "Destroy"}, wantZero: true,
		},
		{
			name:      "rows close",
			configure: func(f *queryTestFixture) { f.probe.closeErr = execution.ErrInvalidQuery },
			wantCalls: []string{"OpenQuery", "Acquire", "BeginTx:readonly", "QueryContext:SELECT 1", "Rows.Close", "Rollback", "Destroy"}, wantZero: true,
		},
		{
			name:      "rollback",
			configure: func(f *queryTestFixture) { f.transaction.rollbackErr = execution.ErrDatabaseUnavailable },
			wantCalls: []string{"OpenQuery", "Acquire", "BeginTx:readonly", "QueryContext:SELECT 1", "Rows.Close", "Rollback", "Destroy"}, wantZero: true,
		},
		{
			name:      "destroy",
			configure: func(f *queryTestFixture) { f.connection.destroyErr = execution.ErrDatabaseUnavailable },
			wantCalls: []string{"OpenQuery", "Acquire", "BeginTx:readonly", "QueryContext:SELECT 1", "Rows.Close", "Rollback", "Destroy"}, wantZero: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newQueryTestFixture(t)
			test.configure(fixture)
			result, err := fixture.executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
				Source: connection.Definition{ID: "finance", Kind: Kind}, Query: "SELECT 1",
			})
			if err == nil {
				t.Fatal("Query() error = nil")
			}
			if !reflect.DeepEqual(fixture.recorder.calls, test.wantCalls) {
				t.Fatalf("calls = %#v, want %#v", fixture.recorder.calls, test.wantCalls)
			}
			if test.wantZero && !reflect.DeepEqual(result, execution.RelationalQueryResult{}) {
				t.Fatalf("result = %#v, want zero result", result)
			}
		})
	}
}

func TestQueryExecutorJoinsPrimaryAndCleanupFailures(t *testing.T) {
	t.Parallel()

	fixture := newQueryTestFixture(t)
	fixture.transaction.queryErr = execution.ErrInvalidQuery
	fixture.transaction.rows = nil
	fixture.transaction.rollbackErr = execution.ErrDatabaseUnavailable
	fixture.connection.destroyErr = execution.ErrDatabaseConflict

	result, err := fixture.executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
		Source: connection.Definition{ID: "finance", Kind: Kind}, Query: "SELECT 1",
	})
	if !errors.Is(err, execution.ErrInvalidQuery) ||
		!errors.Is(err, execution.ErrDatabaseUnavailable) ||
		!errors.Is(err, execution.ErrDatabaseConflict) {
		t.Fatalf("Query() error = %v, want primary + both cleanup failures", err)
	}
	if !reflect.DeepEqual(result, execution.RelationalQueryResult{}) {
		t.Fatalf("result = %#v, want zero result", result)
	}
}
