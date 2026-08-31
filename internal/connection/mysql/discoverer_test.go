package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/DataPorch/dataporch/internal/connection"
	"github.com/DataPorch/dataporch/internal/execution"
	gomysql "github.com/go-sql-driver/mysql"
)

type testClientOpener struct {
	mu       sync.Mutex
	client   *Client
	err      error
	sourceID connection.ID
	openCall int
}

func (o *testClientOpener) Open(ctx context.Context, sourceID connection.ID) (*Client, error) {
	o.mu.Lock()
	o.sourceID = sourceID
	o.openCall++
	client, err := o.client, o.err
	o.mu.Unlock()

	if ctx == nil {
		return nil, errors.New("nil context")
	}

	return client, err
}

type testCatalogPool struct {
	mu           sync.Mutex
	rows         *testCatalogRows
	results      []testCatalogResult
	queryErr     error
	queryCount   int
	arguments    [][]any
	queries      []string
	queryContext []context.Context
}

func (*testCatalogPool) Ping(context.Context) error { return nil }

func (p *testCatalogPool) Query(
	ctx context.Context,
	query string,
	arguments ...any,
) (catalogRows, error) {
	p.mu.Lock()
	queryIndex := p.queryCount
	p.queryCount++
	p.arguments = append(p.arguments, append([]any(nil), arguments...))
	p.queries = append(p.queries, query)
	p.queryContext = append(p.queryContext, ctx)
	rows := p.rows
	queryErr := p.queryErr

	var result testCatalogResult
	if queryIndex < len(p.results) {
		result = p.results[queryIndex]
	}
	p.mu.Unlock()

	if queryIndex < len(p.results) {
		return result.rows, result.err
	}

	if queryErr != nil {
		return nil, queryErr
	}

	if rows == nil {
		rows = &testCatalogRows{}
	}

	return rows, nil
}

func (*testCatalogPool) Close() error { return nil }

type testCatalogResult struct {
	rows *testCatalogRows
	err  error
}

type testCatalogRows struct {
	values     [][]any
	index      int
	current    []any
	scanErr    error
	rowsErr    error
	closeErr   error
	closeCalls int
}

func (r *testCatalogRows) Close() error {
	r.closeCalls++
	return r.closeErr
}

func (r *testCatalogRows) Err() error { return r.rowsErr }

func (r *testCatalogRows) Next() bool {
	if r.index >= len(r.values) {
		return false
	}

	r.current = r.values[r.index]
	r.index++

	return true
}

func (r *testCatalogRows) Scan(destinations ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}

	if len(destinations) != len(r.current) {
		return errors.New("wrong scan destination count")
	}

	for index, destination := range destinations {
		if err := assignTestScanDestination(destination, r.current[index]); err != nil {
			return err
		}
	}

	return nil
}

func assignTestScanDestination(destination, value any) error {
	target := reflect.ValueOf(destination)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return errors.New("scan destination is not a pointer")
	}

	valueTarget := target.Elem()
	if value == nil {
		valueTarget.Set(reflect.Zero(valueTarget.Type()))
		return nil
	}

	if valueTarget.Kind() == reflect.Pointer {
		inner := reflect.New(valueTarget.Type().Elem())
		if err := assignTestScanValue(inner.Elem(), value); err != nil {
			return err
		}

		valueTarget.Set(inner)

		return nil
	}

	return assignTestScanValue(valueTarget, value)
}

func assignTestScanValue(target reflect.Value, value any) error {
	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(target.Type()) {
		target.Set(source)
		return nil
	}

	if source.Type().ConvertibleTo(target.Type()) {
		target.Set(source.Convert(target.Type()))
		return nil
	}

	return errors.New("scan value type mismatch")
}

func TestNewDiscovererValidatesDependencies(t *testing.T) {
	t.Parallel()

	opener := &testClientOpener{}

	if _, err := newDiscoverer(nil, time.Second); !errors.Is(err, errClientOpenerRequired) {
		t.Fatalf("newDiscoverer(nil) error = %v, want %v", err, errClientOpenerRequired)
	}

	if _, err := newDiscoverer(opener, 0); !errors.Is(err, errQueryTimeoutRequired) {
		t.Fatalf("newDiscoverer(timeout=0) error = %v, want %v", err, errQueryTimeoutRequired)
	}

	discoverer, err := NewDiscoverer(opener)
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v", err)
	}

	if discoverer.Kind() != Kind {
		t.Fatalf("Kind() = %q, want %q", discoverer.Kind(), Kind)
	}
}

func TestDiscovererRejectsNilAndCancelledContexts(t *testing.T) {
	t.Parallel()

	opener := &testClientOpener{client: &Client{pool: &testCatalogPool{}}}
	discoverer := lifecycleTestDiscoverer(t, opener)

	_, err := discoverer.ListSchemas(nil, execution.SchemaDiscoveryRequest{SourceID: "finance", Limit: 1}) //nolint:staticcheck // nil context is the validation case under test.
	if !errors.Is(err, execution.ErrCancelled) {
		t.Fatalf("ListSchemas(nil) error = %v, want cancellation", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = discoverer.ListTables(ctx, execution.TableDiscoveryRequest{SourceID: "finance", Schema: "finance", Limit: 1})
	if !errors.Is(err, execution.ErrCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("ListTables(cancelled) error = %v, want cancellation", err)
	}

	opener.mu.Lock()
	openCalls := opener.openCall
	opener.mu.Unlock()

	if openCalls != 0 {
		t.Fatalf("Open calls = %d, want 0 for invalid contexts", openCalls)
	}
}

func TestDiscovererProjectsUnavailableOpenerErrors(t *testing.T) {
	t.Parallel()

	openErr := errors.New("connection refused")
	opener := &testClientOpener{err: openErr}
	discoverer := lifecycleTestDiscoverer(t, opener)

	_, err := discoverer.ListSchemas(t.Context(), execution.SchemaDiscoveryRequest{SourceID: "finance", Limit: 1})
	if !errors.Is(err, execution.ErrDatabaseUnavailable) || !errors.Is(err, openErr) {
		t.Fatalf("ListSchemas() error = %v, want unavailable opener error", err)
	}
}

func TestListTablesProjectsMySQLPermissionErrorsForDiscoveryClassification(t *testing.T) {
	t.Parallel()

	nativeErr := &gomysql.MySQLError{
		Number:   1142,
		SQLState: [5]byte{'4', '2', '0', '0', '0'},
		Message:  "SELECT command denied",
	}
	discoverer := lifecycleTestDiscoverer(t, &testClientOpener{
		client: &Client{pool: &testCatalogPool{queryErr: nativeErr}, database: "finance"},
	})

	_, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID: "finance",
		Schema:   "finance",
		Limit:    1,
	})
	if !errors.Is(err, nativeErr) {
		t.Fatalf("ListTables() error = %v, want native cause", err)
	}

	failure := execution.Classify(err)
	if failure.Category != execution.ErrorCategoryDatabasePermissionDenied {
		t.Fatalf(
			"Classify(ListTables()) category = %q, want %q",
			failure.Category,
			execution.ErrorCategoryDatabasePermissionDenied,
		)
	}
}

func TestClassifyDiscoveryQueryErrorUsesDiscoverySentinels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want execution.ErrorCategory
	}{
		{
			name: "permission denied",
			err:  &gomysql.MySQLError{Number: 1142},
			want: execution.ErrorCategoryDatabasePermissionDenied,
		},
		{
			name: "query timeout",
			err:  &gomysql.MySQLError{Number: 3024},
			want: execution.ErrorCategoryQueryTimeout,
		},
		{
			name: "query cancelled",
			err:  &gomysql.MySQLError{Number: 1317},
			want: execution.ErrorCategoryCancelled,
		},
		{
			name: "database unavailable",
			err:  &gomysql.MySQLError{Number: 1040},
			want: execution.ErrorCategoryDatabaseUnavailable,
		},
		{
			name: "bad connection",
			err:  driver.ErrBadConn,
			want: execution.ErrorCategoryDatabaseUnavailable,
		},
		{
			name: "unknown native error",
			err:  &gomysql.MySQLError{Number: 9999},
			want: execution.ErrorCategoryInternal,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := classifyDiscoveryQueryError(t.Context(), t.Context(), test.err)
			if !errors.Is(err, test.err) {
				t.Fatalf("classifyDiscoveryQueryError() error = %v, want native cause", err)
			}

			failure := execution.Classify(err)
			if failure.Category != test.want {
				t.Fatalf("failure category = %q, want %q", failure.Category, test.want)
			}
		})
	}
}

func lifecycleTestDiscoverer(t *testing.T, opener clientOpener) *Discoverer {
	t.Helper()

	discoverer, err := newDiscoverer(opener, time.Second)
	if err != nil {
		t.Fatalf("newDiscoverer() error = %v", err)
	}

	return discoverer
}

var _ runtimePool = (*testCatalogPool)(nil)
