package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/execution"
)

func TestListColumnsMapsMetadataAndPaginatesByOrdinal(t *testing.T) {
	t.Parallel()

	pool := &testCatalogPool{results: []testCatalogResult{
		{rows: &testCatalogRows{values: [][]any{{true, true}}}},
		{rows: &testCatalogRows{values: [][]any{{uint32(42), "r", true}}}},
		{rows: &testCatalogRows{values: [][]any{
			{"id", 1, "integer", "pg_catalog", "int4", "base", nil, nil, nil, nil, false, nil, nil, nil, nil, false, nil, "a", "", "amount description"},
			{"amount", 4, "numeric(12,2)", "pg_catalog", "numeric", "base", nil, int32(12), int32(2), nil, false, nil, nil, nil, nil, true, "42", "", "", "amount description"},
			{"generated_amount", 5, "numeric", "pg_catalog", "numeric", "base", nil, nil, nil, nil, false, nil, nil, nil, nil, true, "amount * 2", "", "s", "amount description"},
		}}},
		{rows: &testCatalogRows{}},
	}}
	discoverer, err := newDiscoverer(&testClientOpener{client: &Client{pool: pool}}, time.Second)
	if err != nil {
		t.Fatalf("newDiscoverer() error = %v", err)
	}

	page, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID:            "analytics",
		Schema:              "Sales Data",
		Table:               "Customers",
		Search:              `%_*.[x]\\`,
		IncludeDescriptions: true,
		Limit:               2,
		AfterOrdinal:        0,
	})
	if err != nil {
		t.Fatalf("ListColumns() error = %v", err)
	}
	if len(page.Columns) != 2 || !page.HasMore || page.RelationKind != execution.RelationKindTable {
		t.Fatalf("page = %#v, want two columns with more and table kind", page)
	}
	if page.Columns[0].OrdinalPosition != 1 || page.Columns[1].OrdinalPosition != 4 {
		t.Fatalf("ordinals = %d/%d, want 1/4", page.Columns[0].OrdinalPosition, page.Columns[1].OrdinalPosition)
	}
	if page.Columns[0].Identity == nil || page.Columns[0].Identity.Generation != "always" {
		t.Fatalf("identity = %#v, want always", page.Columns[0].Identity)
	}
	if page.Columns[1].Type.Precision == nil || *page.Columns[1].Type.Precision != 12 || page.Columns[1].DefaultExpression == nil || *page.Columns[1].DefaultExpression != "42" {
		t.Fatalf("numeric metadata = %#v", page.Columns[1])
	}
	if pool.allArguments[2][0] != uint32(42) || pool.allArguments[2][1] != `%_*.[x]\\` || pool.allArguments[2][2] != 0 || pool.allArguments[2][3] != true || pool.allArguments[2][4] != 3 {
		t.Fatalf("column query arguments = %#v", pool.allArguments[2])
	}

	generatedRows := &testCatalogRows{values: [][]any{{"generated_amount", 5, "numeric", "pg_catalog", "numeric", "base", nil, nil, nil, nil, false, nil, nil, nil, nil, true, "amount * 2", "", "s", "amount description"}}}
	generatedRows.Next()
	generated, err := scanColumn(generatedRows, true)
	if err != nil {
		t.Fatalf("scanColumn(generated) error = %v", err)
	}
	if generated.Generated == nil || generated.DefaultExpression != nil || generated.Generated.Expression != "amount * 2" {
		t.Fatalf("generated metadata = %#v", generated)
	}
}

func TestListColumnsResolvesParentErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		relation   []any
		want       error
		queryCount int
	}{
		{name: "missing relation", relation: nil, want: execution.ErrRelationNotFound, queryCount: 2},
		{name: "denied relation", relation: []any{uint32(1), "r", false}, want: execution.ErrDatabasePermissionDenied, queryCount: 2},
		{name: "unsupported relation", relation: []any{uint32(1), "x", true}, want: execution.ErrUnsupportedRelationKind, queryCount: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			results := []testCatalogResult{{rows: &testCatalogRows{values: [][]any{{true, true}}}}}
			if test.relation != nil {
				results = append(results, testCatalogResult{rows: &testCatalogRows{values: [][]any{test.relation}}})
			} else {
				results = append(results, testCatalogResult{rows: &testCatalogRows{}})
			}
			pool := &testCatalogPool{results: results}
			discoverer, err := newDiscoverer(&testClientOpener{client: &Client{pool: pool}}, time.Second)
			if err != nil {
				t.Fatalf("newDiscoverer() error = %v", err)
			}
			_, err = discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{SourceID: "analytics", Schema: "public", Table: "customers", Limit: 1})
			if !errors.Is(err, test.want) {
				t.Fatalf("ListColumns() error = %v, want %v", err, test.want)
			}
			if pool.queryCount != test.queryCount {
				t.Fatalf("query count = %d, want %d", pool.queryCount, test.queryCount)
			}
		})
	}
}

func TestTypeCategoryAndColumnMetadataValidation(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"base", "array", "domain", "enum", "composite", "range", "multirange", "pseudo", "other"} {
		if _, err := typeCategory(code); err != nil {
			t.Errorf("typeCategory(%q) error = %v", code, err)
		}
	}
	if _, err := typeCategory("unknown"); !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("typeCategory(unknown) error = %v, want internal", err)
	}
	if identity, err := columnIdentity("d"); err != nil || identity.Generation != "by_default" {
		t.Fatalf("columnIdentity(d) = %#v, %v", identity, err)
	}
	if _, err := columnIdentity("x"); !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("columnIdentity(x) error = %v, want internal", err)
	}
	if generated, err := columnGenerated("s", stringPointer("a + b")); err != nil || generated.Kind != "stored" || generated.Expression != "a + b" {
		t.Fatalf("columnGenerated(s) = %#v, %v", generated, err)
	}
	if _, err := columnGenerated("x", nil); !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("columnGenerated(x) error = %v, want internal", err)
	}
}

func stringPointer(value string) *string { return &value }
