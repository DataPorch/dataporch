package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/execution"
)

func TestListTablesChecksSchemaAndMapsRelations(t *testing.T) {
	t.Parallel()

	pool := &testCatalogPool{results: []testCatalogResult{
		{rows: &testCatalogRows{values: [][]any{{true, true}}}},
		{rows: &testCatalogRows{values: [][]any{
			{"ordinary", "r", "table description"},
			{"partitioned", "p", "partition description"},
		}}},
	}}
	discoverer, err := newDiscoverer(&testClientOpener{client: &Client{pool: pool}}, time.Second)
	if err != nil {
		t.Fatalf("newDiscoverer() error = %v", err)
	}

	page, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID:            "analytics",
		Schema:              "Sales Data",
		Search:              `%_*.[x]\\`,
		IncludeDescriptions: true,
		Limit:               1,
		AfterName:           "before",
	})
	if err != nil {
		t.Fatalf("ListTables() error = %v", err)
	}
	if len(page.Tables) != 1 || page.Tables[0].Kind != execution.RelationKindTable || page.Tables[0].Description == nil || !page.HasMore {
		t.Fatalf("page = %#v, want one described table with more", page)
	}
	if len(pool.allArguments) != 2 || len(pool.allArguments[0]) != 1 || pool.allArguments[0][0] != "Sales Data" {
		t.Fatalf("schema query arguments = %#v", pool.allArguments)
	}
	if got := pool.allArguments[1]; len(got) != 5 || got[0] != "Sales Data" || got[1] != true || got[2] != `%_*.[x]\\` || got[3] != "before" || got[4] != 2 {
		t.Fatalf("table query arguments = %#v", got)
	}
}

func TestListTablesDistinguishesSchemaErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		row  []any
		want error
	}{
		{name: "missing", row: []any{false, false}, want: execution.ErrSchemaNotFound},
		{name: "denied", row: []any{true, false}, want: execution.ErrDatabasePermissionDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := &testCatalogPool{results: []testCatalogResult{{rows: &testCatalogRows{values: [][]any{test.row}}}}}
			discoverer, err := newDiscoverer(&testClientOpener{client: &Client{pool: pool}}, time.Second)
			if err != nil {
				t.Fatalf("newDiscoverer() error = %v", err)
			}
			_, err = discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{SourceID: "analytics", Schema: "private", Limit: 1})
			if !errors.Is(err, test.want) {
				t.Fatalf("ListTables() error = %v, want %v", err, test.want)
			}
			if pool.queryCount != 1 {
				t.Fatalf("query count = %d, want schema check only", pool.queryCount)
			}
		})
	}
}

func TestListTablesRejectsUnknownRelationKindAndQueryFailures(t *testing.T) {
	t.Parallel()

	pool := &testCatalogPool{results: []testCatalogResult{
		{rows: &testCatalogRows{values: [][]any{{true, true}}}},
		{rows: &testCatalogRows{values: [][]any{{"bad", "x", nil}}}},
	}}
	discoverer, err := newDiscoverer(&testClientOpener{client: &Client{pool: pool}}, time.Second)
	if err != nil {
		t.Fatalf("newDiscoverer() error = %v", err)
	}
	_, err = discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{SourceID: "analytics", Schema: "public", Limit: 1})
	if !errors.Is(err, execution.ErrUnsupportedRelationKind) {
		t.Fatalf("unknown relation error = %v, want unsupported kind", err)
	}

	pool = &testCatalogPool{results: []testCatalogResult{
		{rows: &testCatalogRows{values: [][]any{{true, true}}}},
		{err: errors.New("raw query failure")},
	}}
	discoverer, err = newDiscoverer(&testClientOpener{client: &Client{pool: pool}}, time.Second)
	if err != nil {
		t.Fatalf("newDiscoverer(query) error = %v", err)
	}
	_, err = discoverer.ListTables(context.Background(), execution.TableDiscoveryRequest{SourceID: "analytics", Schema: "public", Limit: 1})
	if !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("query error = %v, want internal", err)
	}
}

func TestRelationKindMapping(t *testing.T) {
	t.Parallel()

	tests := map[string]execution.RelationKind{
		"r": execution.RelationKindTable,
		"p": execution.RelationKindPartitionedTable,
		"v": execution.RelationKindView,
		"m": execution.RelationKindMaterializedView,
		"f": execution.RelationKindForeignTable,
	}
	for code, want := range tests {
		got, err := relationKind(code)
		if err != nil || got != want {
			t.Errorf("relationKind(%q) = %q, %v; want %q", code, got, err, want)
		}
	}
}
