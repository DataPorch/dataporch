package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewQueryExecutorValidatesDependenciesAndOptions(t *testing.T) {
	t.Parallel()

	validOptions := QueryOptions{
		Timeout:           time.Second,
		ResponseByteLimit: 65_536,
		TruncationEnabled: true,
		RowLimit:          1,
	}

	tests := []struct {
		name    string
		opener  queryClientOpener
		options QueryOptions
		cleanup time.Duration
		want    error
	}{
		{name: "missing opener", options: validOptions, cleanup: time.Second, want: errQueryOpenerRequired},
		{name: "typed nil opener", opener: (*queryOpenerStub)(nil), options: validOptions, cleanup: time.Second, want: errQueryOpenerRequired},
		{name: "missing timeout", opener: &queryOpenerStub{}, options: QueryOptions{ResponseByteLimit: 1, TruncationEnabled: true, RowLimit: 1}, cleanup: time.Second, want: errRelationalQueryTimeoutRequired},
		{name: "missing byte limit", opener: &queryOpenerStub{}, options: QueryOptions{Timeout: time.Second, TruncationEnabled: true, RowLimit: 1}, cleanup: time.Second, want: errQueryByteLimitRequired},
		{name: "missing cleanup timeout", opener: &queryOpenerStub{}, options: validOptions, cleanup: 0, want: errQueryCleanupRequired},
		{name: "missing row limit", opener: &queryOpenerStub{}, options: QueryOptions{Timeout: time.Second, ResponseByteLimit: 1, TruncationEnabled: true}, cleanup: time.Second, want: errQueryRowLimitRequired},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := newQueryExecutor(test.opener, test.options, test.cleanup); !errors.Is(err, test.want) {
				t.Fatalf("newQueryExecutor() error = %v, want %v", err, test.want)
			}
		})
	}

	executor, err := NewQueryExecutor(&queryOpenerStub{}, validOptions)
	if err != nil {
		t.Fatalf("NewQueryExecutor() error = %v", err)
	}

	if executor.Kind() != Kind {
		t.Fatalf("Kind() = %q, want %q", executor.Kind(), Kind)
	}
}

func TestQueryExecutorUsesReadOnlyTransactionAndExtendedTextMode(t *testing.T) {
	t.Parallel()

	rows := &queryRowsStub{
		fields: []pgconn.FieldDescription{{Name: "value", DataTypeOID: 23}},
		rows:   [][][]byte{{[]byte("1")}},
	}
	executor, opener, pool, connectionStub, transaction := newQueryTestExecutor(t, rows, QueryOptions{
		Timeout:           time.Second,
		ResponseByteLimit: 65_536,
		TruncationEnabled: true,
		RowLimit:          1000,
	})
	connectionStub.typeNames[23] = "int4"

	result, err := executor.Query(t.Context(), queryRequest(" \nSELECT 1;\t"))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	if diff := cmpTxOptions(connectionStub.beginOptions, []pgx.TxOptions{{AccessMode: pgx.ReadOnly}}); diff != "" {
		t.Fatalf("begin options mismatch (-got +want):\n%s", diff)
	}

	if !reflect.DeepEqual(transaction.modes, []pgx.QueryExecMode{pgx.QueryExecModeExec}) {
		t.Fatalf("query modes = %v, want QueryExecModeExec", transaction.modes)
	}

	if !reflect.DeepEqual(transaction.queries, []string{" \nSELECT 1;\t"}) {
		t.Fatalf("queries = %q, want original query", transaction.queries)
	}

	if len(opener.contexts) != 1 || len(pool.contexts) != 1 || len(connectionStub.beginContexts) != 1 || len(transaction.contexts) != 1 {
		t.Fatalf("operation context counts = opener %d/acquire %d/begin %d/query %d, want one each", len(opener.contexts), len(pool.contexts), len(connectionStub.beginContexts), len(transaction.contexts))
	}

	if !reflect.DeepEqual(connectionStub.cleanupOperations, []string{"rollback", "deallocate", "discard", "release"}) {
		t.Fatalf("cleanup operations = %v", connectionStub.cleanupOperations)
	}

	if result.RowCount != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "1" {
		t.Fatalf("result = %#v, want one text row", result)
	}
}

func TestQueryExecutorPassesOriginalQueryUnchanged(t *testing.T) {
	t.Parallel()

	rows := &queryRowsStub{fields: []pgconn.FieldDescription{{Name: "value", DataTypeOID: 23}}}
	executor, _, _, _, transaction := newQueryTestExecutor(t, rows, testQueryOptions())

	query := " \nSELECT 1;\t"
	if _, err := executor.Query(t.Context(), execution.RelationalQueryExecutionRequest{
		Source: connection.Definition{ID: "finance", Kind: Kind},
		Query:  query,
	}); err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	if !reflect.DeepEqual(transaction.queries, []string{query}) {
		t.Fatalf("queries = %q, want %q", transaction.queries, query)
	}
}

func TestQueryExecutorRejectsZeroColumnResults(t *testing.T) {
	t.Parallel()

	executor, _, _, connectionStub, _ := newQueryTestExecutor(t, &queryRowsStub{}, testQueryOptions())

	_, err := executor.Query(t.Context(), queryRequest("SELECT 1"))
	if !errors.Is(err, execution.ErrInvalidQuery) {
		t.Fatalf("Query() error = %v, want ErrInvalidQuery", err)
	}

	if connectionStub.releases != 1 || connectionStub.destroys != 0 {
		t.Fatalf("cleanup release/destroy = %d/%d, want 1/0", connectionStub.releases, connectionStub.destroys)
	}
}

func TestQueryExecutorPreservesColumnsTextAndNulls(t *testing.T) {
	t.Parallel()

	rows := &queryRowsStub{
		fields: []pgconn.FieldDescription{
			{Name: "id", DataTypeOID: 23},
			{Name: "note", DataTypeOID: 25},
		},
		rows: [][][]byte{{[]byte("1"), nil}, {[]byte("2"), []byte("")}},
	}
	executor, _, _, connectionStub, _ := newQueryTestExecutor(t, rows, testQueryOptions())
	connectionStub.typeNames[23] = "int4"
	connectionStub.typeNames[25] = "text"

	result, err := executor.Query(t.Context(), queryRequest("SELECT id, note FROM items"))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	if !reflect.DeepEqual(result.Columns, []execution.RelationalQueryColumn{
		{Name: "id", DatabaseType: "int4"},
		{Name: "note", DatabaseType: "text"},
	}) {
		t.Fatalf("columns = %#v", result.Columns)
	}

	if result.Rows[0][1] != nil || result.Rows[1][1] == nil || *result.Rows[1][1] != "" {
		t.Fatalf("null and empty values = %#v", result.Rows)
	}
}

func TestQueryExecutorPreservesDuplicateColumnNames(t *testing.T) {
	t.Parallel()

	rows := &queryRowsStub{
		fields: []pgconn.FieldDescription{{Name: "value", DataTypeOID: 25}, {Name: "value", DataTypeOID: 25}},
		rows:   [][][]byte{{[]byte("left"), []byte("right")}},
	}
	executor, _, _, _, _ := newQueryTestExecutor(t, rows, testQueryOptions())

	result, err := executor.Query(t.Context(), queryRequest("SELECT 1, 2"))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	if result.Columns[0].Name != result.Columns[1].Name || *result.Rows[0][0] != "left" || *result.Rows[0][1] != "right" {
		t.Fatalf("duplicate columns/result = %#v/%#v", result.Columns, result.Rows)
	}
}

func TestQueryExecutorUsesUnknownOIDFallback(t *testing.T) {
	t.Parallel()

	rows := &queryRowsStub{
		fields: []pgconn.FieldDescription{{Name: "unknown", DataTypeOID: 999999}},
		rows:   [][][]byte{{[]byte("value")}},
	}
	executor, _, _, _, _ := newQueryTestExecutor(t, rows, testQueryOptions())

	result, err := executor.Query(t.Context(), queryRequest("SELECT value"))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	if result.Columns[0].DatabaseType != "oid:999999" {
		t.Fatalf("unknown database type = %q", result.Columns[0].DatabaseType)
	}
}

func TestQueryExecutorCopiesRawValuesBeforeNext(t *testing.T) {
	t.Parallel()

	rows := &aliasingQueryRowsStub{
		fields: []pgconn.FieldDescription{{Name: "value", DataTypeOID: 25}},
		values: [][]byte{[]byte("first"), []byte("second")},
	}
	executor, _, _, _, _ := newQueryTestExecutor(t, rows, testQueryOptions())

	result, err := executor.Query(t.Context(), queryRequest("SELECT value"))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	if got := *result.Rows[0][0]; got != "first" {
		t.Fatalf("first value = %q, want copied first value", got)
	}
}

func TestQueryExecutorDetectsTruncationWithOneExtraRow(t *testing.T) {
	t.Parallel()

	rows := &queryRowsStub{
		fields: []pgconn.FieldDescription{{Name: "value", DataTypeOID: 25}},
		rows:   [][][]byte{{[]byte("one")}, {[]byte("two")}},
	}
	options := testQueryOptions()
	options.RowLimit = 1
	executor, _, _, _, _ := newQueryTestExecutor(t, rows, options)

	result, err := executor.Query(t.Context(), queryRequest("SELECT value"))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	if len(result.Rows) != 1 || !result.Truncated || rows.index != 2 {
		t.Fatalf("result/observed rows = %#v/%d, want one retained and two observed", result, rows.index)
	}
}

func TestQueryExecutorDisablesOnlyRowTruncation(t *testing.T) {
	t.Parallel()

	rows := &queryRowsStub{
		fields: []pgconn.FieldDescription{{Name: "value", DataTypeOID: 25}},
		rows:   [][][]byte{{[]byte("one")}, {[]byte("two")}},
	}
	options := testQueryOptions()
	options.TruncationEnabled = false
	options.RowLimit = 0
	executor, _, _, _, _ := newQueryTestExecutor(t, rows, options)

	result, err := executor.Query(t.Context(), queryRequest("SELECT value"))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	if len(result.Rows) != 2 || result.Truncated || result.RowCount != 2 {
		t.Fatalf("result = %#v, want both rows without truncation", result)
	}
}

func TestQueryExecutorRejectsMetadataOverByteLimit(t *testing.T) {
	t.Parallel()

	rows := &queryRowsStub{
		fields: []pgconn.FieldDescription{{Name: "a very long column name", DataTypeOID: 25}},
	}
	options := testQueryOptions()
	options.ResponseByteLimit = 1
	executor, _, _, _, _ := newQueryTestExecutor(t, rows, options)

	_, err := executor.Query(t.Context(), queryRequest("SELECT value"))
	if !errors.Is(err, execution.ErrResultTooLarge) {
		t.Fatalf("Query() error = %v, want ErrResultTooLarge", err)
	}
}

func TestQueryExecutorRejectsRowBeforeRetention(t *testing.T) {
	t.Parallel()

	rows := &queryRowsStub{
		fields: []pgconn.FieldDescription{{Name: "value", DataTypeOID: 25}},
		rows:   [][][]byte{{[]byte("a very long value")}},
	}
	baseResult := execution.RelationalQueryResult{
		Kind:     Kind,
		SourceID: "finance",
		Columns:  []execution.RelationalQueryColumn{{Name: "value", DatabaseType: "oid:25"}},
		Rows:     [][]*string{},
	}

	baseEncoded, err := json.Marshal(baseResult)
	if err != nil {
		t.Fatalf("Marshal(empty result) error = %v", err)
	}

	fullValue := "a very long value"
	baseResult.Rows = [][]*string{{&fullValue}}

	fullEncoded, err := json.Marshal(baseResult)
	if err != nil {
		t.Fatalf("Marshal(full result) error = %v", err)
	}

	options := testQueryOptions()

	options.ResponseByteLimit = len(fullEncoded) - 1
	if options.ResponseByteLimit <= len(baseEncoded) {
		t.Fatalf("test byte limit = %d, want between empty %d and full %d", options.ResponseByteLimit, len(baseEncoded), len(fullEncoded))
	}

	executor, _, _, _, _ := newQueryTestExecutor(t, rows, options)

	_, err = executor.Query(t.Context(), queryRequest("SELECT value"))
	if !errors.Is(err, execution.ErrResultTooLarge) {
		t.Fatalf("Query() error = %v, want ErrResultTooLarge", err)
	}

	if rows.rawCalls != 1 {
		t.Fatalf("raw value calls = %d, want one inspection before rejection", rows.rawCalls)
	}
}

func TestQueryExecutorClosesRowsAndReturnsTerminalError(t *testing.T) {
	t.Parallel()

	terminalErr := &pgconn.PgError{Code: "57014", Message: "cancelled by server"}
	rows := &queryRowsStub{
		fields:      []pgconn.FieldDescription{{Name: "value", DataTypeOID: 25}},
		rows:        [][][]byte{{[]byte("value")}},
		terminalErr: terminalErr,
	}
	executor, _, _, _, _ := newQueryTestExecutor(t, rows, testQueryOptions())

	_, err := executor.Query(t.Context(), queryRequest("SELECT value"))
	if !errors.Is(err, terminalErr) && !errors.As(err, new(*execution.DatabaseError)) {
		t.Fatalf("Query() error = %v, want projected terminal database error", err)
	}

	failure := execution.ClassifyRelationalQuery(t.Context(), err)
	if failure.Category != execution.ErrorCategoryQueryCancelled {
		t.Fatalf("terminal failure = %#v, want query_cancelled", failure)
	}

	if !rows.closed {
		t.Fatal("rows closed = false, want true")
	}
}

func TestQueryExecutorProjectsOpenAcquireBeginQueryAndRowErrors(t *testing.T) {
	t.Parallel()

	pgError := &pgconn.PgError{Code: "42501", Message: "permission denied"}
	tests := []struct {
		name string
		set  func(*queryOpenerStub, *queryPoolStub, *queryConnectionStub, *queryTransactionStub, *queryRowsStub)
		want execution.ErrorCategory
	}{
		{name: "open", set: func(opener *queryOpenerStub, _ *queryPoolStub, _ *queryConnectionStub, _ *queryTransactionStub, _ *queryRowsStub) {
			opener.err = pgError
		}, want: execution.ErrorCategoryDatabasePermissionDenied},
		{name: "acquire", set: func(_ *queryOpenerStub, pool *queryPoolStub, _ *queryConnectionStub, _ *queryTransactionStub, _ *queryRowsStub) {
			pool.acquireErr = pgError
		}, want: execution.ErrorCategoryDatabasePermissionDenied},
		{name: "begin", set: func(_ *queryOpenerStub, _ *queryPoolStub, connectionStub *queryConnectionStub, _ *queryTransactionStub, _ *queryRowsStub) {
			connectionStub.beginErr = pgError
		}, want: execution.ErrorCategoryDatabasePermissionDenied},
		{name: "query", set: func(_ *queryOpenerStub, _ *queryPoolStub, _ *queryConnectionStub, transaction *queryTransactionStub, _ *queryRowsStub) {
			transaction.queryErr = pgError
		}, want: execution.ErrorCategoryDatabasePermissionDenied},
		{name: "rows", set: func(_ *queryOpenerStub, _ *queryPoolStub, _ *queryConnectionStub, _ *queryTransactionStub, rows *queryRowsStub) {
			rows.terminalErr = pgError
		}, want: execution.ErrorCategoryDatabasePermissionDenied},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			opener, pool, connectionStub, transaction, rows := newQueryRuntimeFakes()
			test.set(opener, pool, connectionStub, transaction, rows)
			executor := newQueryExecutorForFakes(t, opener)

			_, err := executor.Query(t.Context(), queryRequest("SELECT value"))

			failure := execution.ClassifyRelationalQuery(t.Context(), err)
			if failure.Category != test.want {
				t.Fatalf("failure = %#v, want %q (error %v)", failure, test.want, err)
			}
		})
	}
}

func TestQueryExecutorBoundsEveryOperationWithQueryContext(t *testing.T) {
	t.Parallel()

	opener, pool, connectionStub, transaction, rows := newQueryRuntimeFakes()

	executor := newQueryExecutorForFakes(t, opener)
	if _, err := executor.Query(t.Context(), queryRequest("SELECT value")); err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	contexts := []context.Context{opener.contexts[0], pool.contexts[0], connectionStub.beginContexts[0], transaction.contexts[0]}
	for index, ctx := range contexts {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatalf("operation context %d has no deadline", index)
		}

		if ctx == t.Context() {
			t.Fatalf("operation context %d reused request context", index)
		}
	}

	if len(rows.fields) == 0 {
		t.Fatal("rows fixture unexpectedly has no fields")
	}
}

func TestQueryExecutorCleanupSurvivesRequestCancellation(t *testing.T) {
	t.Parallel()

	opener, pool, connectionStub, transaction, _ := newQueryRuntimeFakes()
	executor := newQueryExecutorForFakes(t, opener)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	if err := executor.cleanup(canceled, connectionStub, transaction); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}

	if len(pool.contexts) != 0 {
		t.Fatal("unused pool context should remain empty")
	}

	if len(transaction.rollbackContexts) != 1 || transaction.rollbackContextErrors[0] != nil {
		t.Fatalf("cleanup context = %#v, err = %v, want independent non-canceled context", transaction.rollbackContexts, transaction.rollbackContextErrors[0])
	}

	if connectionStub.releases != 1 || connectionStub.destroys != 0 {
		t.Fatalf("release/destroy = %d/%d, want 1/0", connectionStub.releases, connectionStub.destroys)
	}
}

func TestQueryExecutorCleanupDisposition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		rollbackErr     error
		deallocateErr   error
		discardErr      error
		wantOperations  []string
		wantRelease     int
		wantDestroy     int
		wantCallFailure bool
	}{
		{name: "clean", wantOperations: []string{"rollback", "deallocate", "discard", "release"}, wantRelease: 1},
		{name: "rollback fails", rollbackErr: errors.New("rollback failed"), wantOperations: []string{"rollback", "destroy"}, wantDestroy: 1, wantCallFailure: true},
		{name: "deallocate fails", deallocateErr: errors.New("deallocate failed"), wantOperations: []string{"rollback", "deallocate", "destroy"}, wantDestroy: 1, wantCallFailure: true},
		{name: "discard fails", discardErr: errors.New("discard failed"), wantOperations: []string{"rollback", "deallocate", "discard", "destroy"}, wantDestroy: 1, wantCallFailure: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			opener, _, connectionStub, transaction, _ := newQueryRuntimeFakes()
			connectionStub.deallocateErr = test.deallocateErr
			connectionStub.discardErr = test.discardErr
			transaction.rollbackErr = test.rollbackErr
			executor := newQueryExecutorForFakes(t, opener)

			err := executor.cleanup(t.Context(), connectionStub, transaction)
			if (err != nil) != test.wantCallFailure {
				t.Fatalf("cleanup() error = %v, want error %t", err, test.wantCallFailure)
			}

			if !reflect.DeepEqual(connectionStub.cleanupOperations, test.wantOperations) {
				t.Fatalf("cleanup operations = %v, want %v", connectionStub.cleanupOperations, test.wantOperations)
			}

			if connectionStub.releases != test.wantRelease || connectionStub.destroys != test.wantDestroy {
				t.Fatalf("release/destroy = %d/%d, want %d/%d", connectionStub.releases, connectionStub.destroys, test.wantRelease, test.wantDestroy)
			}
		})
	}
}

func queryRequest(query string) execution.RelationalQueryExecutionRequest {
	return execution.RelationalQueryExecutionRequest{
		Source: connection.Definition{ID: "finance", Kind: Kind},
		Query:  query,
	}
}

func testQueryOptions() QueryOptions {
	return QueryOptions{
		Timeout:           time.Second,
		ResponseByteLimit: 1 << 20,
		TruncationEnabled: true,
		RowLimit:          1000,
	}
}

func newQueryTestExecutor(
	t *testing.T,
	rows queryRows,
	options QueryOptions,
) (*QueryExecutor, *queryOpenerStub, *queryPoolStub, *queryConnectionStub, *queryTransactionStub) {
	t.Helper()

	opener, pool, connectionStub, transaction, _ := newQueryRuntimeFakes()
	transaction.rows = rows

	executor, err := newQueryExecutor(opener, options, time.Second)
	if err != nil {
		t.Fatalf("newQueryExecutor() error = %v", err)
	}

	return executor, opener, pool, connectionStub, transaction
}

func newQueryExecutorForFakes(t *testing.T, opener *queryOpenerStub) *QueryExecutor {
	t.Helper()

	executor, err := newQueryExecutor(opener, testQueryOptions(), time.Second)
	if err != nil {
		t.Fatalf("newQueryExecutor() error = %v", err)
	}

	return executor
}

func newQueryRuntimeFakes() (*queryOpenerStub, *queryPoolStub, *queryConnectionStub, *queryTransactionStub, *queryRowsStub) {
	rows := &queryRowsStub{fields: []pgconn.FieldDescription{{Name: "value", DataTypeOID: 25}}, rows: [][][]byte{{[]byte("value")}}}
	transaction := &queryTransactionStub{rows: rows}
	connectionStub := &queryConnectionStub{transaction: transaction, typeNames: map[uint32]string{}}
	transaction.owner = connectionStub
	pool := &queryPoolStub{connection: connectionStub}
	opener := &queryOpenerStub{client: &Client{pool: pool}}

	return opener, pool, connectionStub, transaction, rows
}

func cmpTxOptions(got, want []pgx.TxOptions) string {
	if reflect.DeepEqual(got, want) {
		return ""
	}

	return fmt.Sprintf("got %#v, want %#v", got, want)
}

type queryOpenerStub struct {
	client   *Client
	err      error
	contexts []context.Context
	ids      []connection.ID
}

func (o *queryOpenerStub) OpenQuery(ctx context.Context, id connection.ID) (*Client, error) {
	o.contexts = append(o.contexts, ctx)
	o.ids = append(o.ids, id)

	return o.client, o.err
}

type queryPoolStub struct {
	connection *queryConnectionStub
	acquireErr error
	contexts   []context.Context
}

func (p *queryPoolStub) Ping(context.Context) error { return nil }

func (p *queryPoolStub) Query(context.Context, string, ...any) (catalogRows, error) {
	return nil, errors.New("catalog query is not used by relational querying")
}

func (p *queryPoolStub) Close() {}

func (p *queryPoolStub) Acquire(ctx context.Context) (queryConnection, error) {
	p.contexts = append(p.contexts, ctx)
	return p.connection, p.acquireErr
}

type queryConnectionStub struct {
	transaction       *queryTransactionStub
	beginErr          error
	typeNames         map[uint32]string
	beginOptions      []pgx.TxOptions
	beginContexts     []context.Context
	deallocateErr     error
	discardErr        error
	destroyErr        error
	cleanupOperations []string
	releases          int
	destroys          int
}

func (c *queryConnectionStub) BeginTx(ctx context.Context, options pgx.TxOptions) (queryTransaction, error) {
	c.beginOptions = append(c.beginOptions, options)

	c.beginContexts = append(c.beginContexts, ctx)
	if c.transaction != nil {
		c.transaction.owner = c
	}

	return c.transaction, c.beginErr
}

func (c *queryConnectionStub) DatabaseTypeName(oid uint32) (string, bool) {
	name, ok := c.typeNames[oid]
	return name, ok
}

func (c *queryConnectionStub) DeallocateAll(context.Context) error {
	c.cleanupOperations = append(c.cleanupOperations, "deallocate")
	return c.deallocateErr
}

func (c *queryConnectionStub) DiscardAll(context.Context) error {
	c.cleanupOperations = append(c.cleanupOperations, "discard")
	return c.discardErr
}

func (c *queryConnectionStub) Release() {
	c.cleanupOperations = append(c.cleanupOperations, "release")
	c.releases++
}

func (c *queryConnectionStub) Destroy(context.Context) error {
	c.cleanupOperations = append(c.cleanupOperations, "destroy")
	c.destroys++

	return c.destroyErr
}

type queryTransactionStub struct {
	owner                 *queryConnectionStub
	rows                  queryRows
	queryErr              error
	rollbackErr           error
	queries               []string
	modes                 []pgx.QueryExecMode
	contexts              []context.Context
	rollbackContexts      []context.Context
	rollbackContextErrors []error
	rollbackCalls         int
}

func (t *queryTransactionStub) Query(ctx context.Context, query string, mode pgx.QueryExecMode) (queryRows, error) {
	t.queries = append(t.queries, query)
	t.modes = append(t.modes, mode)
	t.contexts = append(t.contexts, ctx)

	return t.rows, t.queryErr
}

func (t *queryTransactionStub) Rollback(ctx context.Context) error {
	t.rollbackCalls++
	t.rollbackContexts = append(t.rollbackContexts, ctx)

	t.rollbackContextErrors = append(t.rollbackContextErrors, ctx.Err())
	if t.owner != nil {
		t.owner.cleanupOperations = append(t.owner.cleanupOperations, "rollback")
	}

	return t.rollbackErr
}

type queryRowsStub struct {
	fields      []pgconn.FieldDescription
	rows        [][][]byte
	terminalErr error
	index       int
	closed      bool
	exhausted   bool
	rawCalls    int
}

func (r *queryRowsStub) Close() {
	r.closed = true
}

func (r *queryRowsStub) Err() error {
	if r.terminalErr != nil && (r.closed || r.exhausted) {
		return r.terminalErr
	}

	return nil
}

func (r *queryRowsStub) FieldDescriptions() []pgconn.FieldDescription {
	return r.fields
}

func (r *queryRowsStub) Next() bool {
	if r.index >= len(r.rows) {
		r.exhausted = true
		return false
	}

	r.index++

	return true
}

func (r *queryRowsStub) RawValues() [][]byte {
	r.rawCalls++
	if r.index == 0 || r.index > len(r.rows) {
		return nil
	}

	return r.rows[r.index-1]
}

type aliasingQueryRowsStub struct {
	fields []pgconn.FieldDescription
	values [][]byte
	index  int
	closed bool
}

func (r *aliasingQueryRowsStub) Close() { r.closed = true }

func (r *aliasingQueryRowsStub) Err() error { return nil }

func (r *aliasingQueryRowsStub) FieldDescriptions() []pgconn.FieldDescription { return r.fields }

func (r *aliasingQueryRowsStub) Next() bool {
	if r.index >= len(r.values) {
		return false
	}

	if r.index > 0 {
		r.values[r.index-1] = []byte("mutated")
	}

	r.index++

	return true
}

func (r *aliasingQueryRowsStub) RawValues() [][]byte {
	return [][]byte{r.values[r.index-1]}
}
