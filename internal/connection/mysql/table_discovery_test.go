package mysql

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/execution"
)

func TestListTablesUsesBoundedParameterizedMySQLCatalogQuery(t *testing.T) {
	t.Parallel()

	rows := &testCatalogRows{values: [][]any{
		{"orders", "BASE TABLE", "Orders"},
		{"orders_view", "VIEW", "Orders view"},
	}}
	pool := &testCatalogPool{rows: rows}
	opener := &testClientOpener{client: &Client{pool: pool, database: "Sales Data"}}
	discoverer := lifecycleTestDiscoverer(t, opener)

	search := `%_literal`
	page, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID:            "analytics",
		Schema:              "Sales Data",
		Search:              search,
		IncludeDescriptions: true,
		Limit:               1,
		AfterName:           "before",
	})
	if err != nil {
		t.Fatalf("ListTables() error = %v", err)
	}

	if len(page.Tables) != 1 || page.Tables[0].Name != "orders" ||
		page.Tables[0].Kind != execution.RelationKindTable ||
		page.Tables[0].Description == nil || *page.Tables[0].Description != "Orders" || !page.HasMore {
		t.Fatalf("page = %#v, want one described table with more", page)
	}

	pool.mu.Lock()
	arguments := append([]any(nil), pool.arguments[0]...)
	query := pool.queries[0]
	queryContext := pool.queryContext[0]
	pool.mu.Unlock()

	wantArguments := []any{true, "Sales Data", search, search, "before", "before", 2}
	if !reflect.DeepEqual(arguments, wantArguments) {
		t.Fatalf("query arguments = %#v, want %#v", arguments, wantArguments)
	}
	if strings.Contains(query, search) {
		t.Fatalf("query contains user search %q: %s", search, query)
	}
	if !strings.Contains(query, "FROM INFORMATION_SCHEMA.TABLES") ||
		!strings.Contains(query, "TABLE_TYPE IN ('BASE TABLE', 'VIEW')") {
		t.Fatalf("query = %s", query)
	}
	if deadline, ok := queryContext.Deadline(); !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Second {
		t.Fatalf("query deadline = %v, want roughly one second", deadline)
	}
	if rows.closeCalls != 1 {
		t.Fatalf("rows close calls = %d, want 1", rows.closeCalls)
	}
}

func TestListTablesRejectsOutsideImportedSchemaBeforeCatalogAccess(t *testing.T) {
	t.Parallel()

	pool := &testCatalogPool{}
	opener := &testClientOpener{client: &Client{pool: pool, database: "finance"}}
	discoverer := lifecycleTestDiscoverer(t, opener)

	_, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID: "finance",
		Schema:   "other_database",
		Limit:    1,
	})
	if !errors.Is(err, execution.ErrSchemaNotFound) {
		t.Fatalf("ListTables() error = %v, want schema not found", err)
	}

	pool.mu.Lock()
	queryCount := pool.queryCount
	pool.mu.Unlock()
	if queryCount != 0 {
		t.Fatalf("catalog query count = %d, want 0", queryCount)
	}
}

func TestListTablesMapsViewsAndRejectsUnknownRelationKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tableType string
		wantKind  execution.RelationKind
		wantErr   error
	}{
		{name: "base table", tableType: "BASE TABLE", wantKind: execution.RelationKindTable},
		{name: "view", tableType: "VIEW", wantKind: execution.RelationKindView},
		{name: "unknown", tableType: "SYSTEM VIEW", wantErr: execution.ErrUnsupportedRelationKind},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := &testCatalogRows{values: [][]any{{"items", test.tableType, nil}}}
			pool := &testCatalogPool{rows: rows}
			opener := &testClientOpener{client: &Client{pool: pool, database: "finance"}}
			discoverer := lifecycleTestDiscoverer(t, opener)

			page, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
				SourceID: "finance",
				Schema:   "finance",
				Limit:    1,
			})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("ListTables() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil || len(page.Tables) != 1 || page.Tables[0].Kind != test.wantKind {
				t.Fatalf("page = %#v, error = %v, want kind %v", page, err, test.wantKind)
			}
		})
	}
}

func TestListTablesHandlesRowsFailuresAndCloseErrors(t *testing.T) {
	t.Parallel()

	rowErr := errors.New("row failure")
	closeErr := errors.New("close failure")
	rows := &testCatalogRows{rowsErr: rowErr, closeErr: closeErr}
	pool := &testCatalogPool{rows: rows}
	opener := &testClientOpener{client: &Client{pool: pool, database: "finance"}}
	discoverer := lifecycleTestDiscoverer(t, opener)

	_, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID: "finance",
		Schema:   "finance",
		Limit:    1,
	})
	if !errors.Is(err, rowErr) || !errors.Is(err, closeErr) || !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("ListTables() error = %v, want row, close, and internal errors", err)
	}
	if rows.closeCalls != 1 {
		t.Fatalf("rows close calls = %d, want 1", rows.closeCalls)
	}
}

func TestListTablesRejectsNilRows(t *testing.T) {
	t.Parallel()

	pool := &testCatalogPool{results: []testCatalogResult{{rows: nil}}}
	opener := &testClientOpener{client: &Client{pool: pool, database: "finance"}}
	discoverer := lifecycleTestDiscoverer(t, opener)

	_, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID: "finance",
		Schema:   "finance",
		Limit:    1,
	})
	if !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("ListTables() error = %v, want internal nil-row error", err)
	}
}
