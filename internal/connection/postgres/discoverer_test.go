package postgres

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewDiscovererValidatesDependencies(t *testing.T) {
	t.Parallel()

	var typedNil *testClientOpener
	if _, err := NewDiscoverer(nil); !errors.Is(err, errClientOpenerRequired) {
		t.Fatalf("NewDiscoverer(nil) error = %v, want opener validation", err)
	}
	if _, err := NewDiscoverer(typedNil); !errors.Is(err, errClientOpenerRequired) {
		t.Fatalf("NewDiscoverer(typed nil) error = %v, want opener validation", err)
	}
	if _, err := newDiscoverer(&testClientOpener{}, 0); !errors.Is(err, errQueryTimeoutRequired) {
		t.Fatalf("newDiscoverer(timeout 0) error = %v, want timeout validation", err)
	}
	if got := (&Discoverer{queryTimeout: time.Second}).Kind(); got != Kind {
		t.Fatalf("Kind() = %q, want %q", got, Kind)
	}
}

func TestListSchemasUsesBoundedParameterizedQuery(t *testing.T) {
	t.Parallel()

	pool := &testCatalogPool{rows: &testCatalogRows{values: [][]any{
		{"public", "should be hidden"},
		{"sales", "Sales schema"},
	}}}
	opener := &testClientOpener{client: &Client{pool: pool}}
	discoverer, err := newDiscoverer(opener, time.Second)
	if err != nil {
		t.Fatalf("newDiscoverer() error = %v", err)
	}

	page, err := discoverer.ListSchemas(t.Context(), execution.SchemaDiscoveryRequest{
		SourceID:            "analytics",
		Search:              `%_*.[x]\\`,
		IncludeDescriptions: false,
		Limit:               1,
		AfterName:           "before",
	})
	if err != nil {
		t.Fatalf("ListSchemas() error = %v", err)
	}
	if len(page.Schemas) != 1 || page.Schemas[0].Name != "public" || page.Schemas[0].Description != nil || !page.HasMore {
		t.Fatalf("page = %#v, want one description-free schema with more", page)
	}
	if opener.sourceID != "analytics" || opener.openDeadline {
		t.Fatalf("opener source/deadline = %q/%t", opener.sourceID, opener.openDeadline)
	}
	if len(pool.arguments) != 4 || pool.arguments[0] != false || pool.arguments[1] != `%_*.[x]\\` || pool.arguments[2] != "before" || pool.arguments[3] != 2 {
		t.Fatalf("query arguments = %#v", pool.arguments)
	}
	if pool.queryDeadline.IsZero() || time.Until(pool.queryDeadline) > time.Second || time.Until(pool.queryDeadline) <= 0 {
		t.Fatalf("query deadline = %v, want roughly one second", pool.queryDeadline)
	}
	if pool.rows.closeCalls != 1 {
		t.Fatalf("rows close calls = %d, want 1", pool.rows.closeCalls)
	}
}

func TestListSchemasSanitizesQueryAndRowFailures(t *testing.T) {
	t.Parallel()

	queryError := errors.New("password=secret host=private catalog failure")
	pool := &testCatalogPool{queryErr: queryError}
	discoverer, err := newDiscoverer(&testClientOpener{client: &Client{pool: pool}}, time.Second)
	if err != nil {
		t.Fatalf("newDiscoverer() error = %v", err)
	}
	_, err = discoverer.ListSchemas(t.Context(), execution.SchemaDiscoveryRequest{SourceID: "analytics", Limit: 1})
	if !errors.Is(err, execution.ErrInternal) || execution.Classify(err).Message == "" {
		t.Fatalf("query error = %v, want sanitized internal classification", err)
	}

	rowError := errors.New("raw scan details")
	pool = &testCatalogPool{rows: &testCatalogRows{scanErr: rowError, values: [][]any{{"public", nil}}}}
	discoverer, err = newDiscoverer(&testClientOpener{client: &Client{pool: pool}}, time.Second)
	if err != nil {
		t.Fatalf("newDiscoverer(row) error = %v", err)
	}
	_, err = discoverer.ListSchemas(t.Context(), execution.SchemaDiscoveryRequest{SourceID: "analytics", Limit: 1})
	if !errors.Is(err, execution.ErrInternal) || execution.Classify(err).Message == "" {
		t.Fatalf("row error = %v, want sanitized internal classification", err)
	}
}

func TestListSchemasClassifiesTimeoutPermissionAndCancellation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "timeout", err: &pgconn.PgError{Code: "57014"}, want: execution.ErrQueryTimeout},
		{name: "permission", err: &pgconn.PgError{Code: "42501"}, want: execution.ErrDatabasePermissionDenied},
		{name: "unavailable", err: &pgconn.PgError{Code: "08006"}, want: execution.ErrDatabaseUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := &testCatalogPool{queryErr: test.err}
			discoverer, err := newDiscoverer(&testClientOpener{client: &Client{pool: pool}}, time.Second)
			if err != nil {
				t.Fatalf("newDiscoverer() error = %v", err)
			}
			_, err = discoverer.ListSchemas(t.Context(), execution.SchemaDiscoveryRequest{SourceID: "analytics", Limit: 1})
			if !errors.Is(err, test.want) {
				t.Fatalf("ListSchemas() error = %v, want %v", err, test.want)
			}
		})
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	discoverer, err := newDiscoverer(&testClientOpener{client: &Client{pool: &testCatalogPool{}}}, time.Second)
	if err != nil {
		t.Fatalf("newDiscoverer(cancel) error = %v", err)
	}
	_, err = discoverer.ListSchemas(ctx, execution.SchemaDiscoveryRequest{SourceID: "analytics", Limit: 1})
	if !errors.Is(err, execution.ErrCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v, want cancellation classification", err)
	}
}

type testClientOpener struct {
	client       *Client
	sourceID     connection.ID
	openDeadline bool
}

func (o *testClientOpener) Open(ctx context.Context, sourceID connection.ID) (*Client, error) {
	o.sourceID = sourceID
	_, o.openDeadline = ctx.Deadline()
	return o.client, nil
}

type testCatalogPool struct {
	mu            sync.Mutex
	rows          *testCatalogRows
	queryErr      error
	arguments     []any
	queryDeadline time.Time
}

func (p *testCatalogPool) Ping(context.Context) error { return nil }

func (p *testCatalogPool) Query(ctx context.Context, _ string, arguments ...any) (catalogRows, error) {
	p.mu.Lock()
	p.arguments = append([]any(nil), arguments...)
	p.queryDeadline, _ = ctx.Deadline()
	p.mu.Unlock()
	if p.queryErr != nil {
		return nil, p.queryErr
	}
	if p.rows == nil {
		p.rows = &testCatalogRows{}
	}
	return p.rows, nil
}

func (p *testCatalogPool) Close() {}

type testCatalogRows struct {
	values     [][]any
	index      int
	current    []any
	scanErr    error
	rowsErr    error
	closeCalls int
}

func (r *testCatalogRows) Close() { r.closeCalls++ }

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
		if err := assignScanDestination(destination, r.current[index]); err != nil {
			return err
		}
	}
	return nil
}

func assignScanDestination(destination any, value any) error {
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
		if err := assignScanValue(inner.Elem(), value); err != nil {
			return err
		}
		valueTarget.Set(inner)
		return nil
	}
	return assignScanValue(valueTarget, value)
}

func assignScanValue(target reflect.Value, value any) error {
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

var _ runtimePool = (*testCatalogPool)(nil)
