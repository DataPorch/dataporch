package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/adamraziv/dataporch/internal/execution"
	sqlite3 "github.com/ncruces/go-sqlite3"
	statementext "github.com/ncruces/go-sqlite3/ext/statement"
)

func TestTypeAffinity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		declared string
		want     execution.TypeAffinity
	}{
		{declared: "INTEGER", want: execution.TypeAffinityInteger},
		{declared: "INT2", want: execution.TypeAffinityInteger},
		{declared: "CHARACTER(20)", want: execution.TypeAffinityText},
		{declared: "CLOB", want: execution.TypeAffinityText},
		{declared: "TEXT", want: execution.TypeAffinityText},
		{declared: "BLOB", want: execution.TypeAffinityBlob},
		{declared: "", want: execution.TypeAffinityBlob},
		{declared: "REAL", want: execution.TypeAffinityReal},
		{declared: "FLOAT", want: execution.TypeAffinityReal},
		{declared: "DOUBLE PRECISION", want: execution.TypeAffinityReal},
		{declared: "NUMERIC", want: execution.TypeAffinityNumeric},
		{declared: "DECIMAL(10,2)", want: execution.TypeAffinityNumeric},
	}

	for _, test := range tests {
		t.Run(test.declared, func(t *testing.T) {
			if got := typeAffinity(test.declared); got != test.want {
				t.Fatalf("typeAffinity(%q) = %q, want %q", test.declared, got, test.want)
			}
		})
	}
}

func TestDiscovererListColumnsMapsMetadataAndPaginatesByOrdinal(t *testing.T) {
	t.Parallel()

	discoverer := newColumnDiscoverer(t)
	page, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID: "columns",
		Schema:   "main",
		Table:    "column_subject",
		Limit:    4,
	})
	if err != nil {
		t.Fatalf("ListColumns() error = %v", err)
	}
	if page.RelationKind != execution.RelationKindTable || !page.HasMore || len(page.Columns) != 4 {
		t.Fatalf("ListColumns() = %#v, want table relation and four-column lookahead page", page)
	}
	if !reflect.DeepEqual(page.Constraints, []execution.Constraint{}) {
		t.Fatalf("ListColumns().Constraints = %#v, want initialized empty slice", page.Constraints)
	}

	all, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID: "columns",
		Schema:   "main",
		Table:    "column_subject",
		Limit:    20,
	})
	if err != nil {
		t.Fatalf("ListColumns(all) error = %v", err)
	}
	if all.HasMore || len(all.Columns) != 8 {
		t.Fatalf("ListColumns(all) = %#v, want eight columns without more", all)
	}

	want := map[string]struct {
		formatted string
		affinity  execution.TypeAffinity
		nullable  bool
		defaultV  *string
		generated string
	}{
		"id":                {formatted: "INTEGER", affinity: execution.TypeAffinityInteger, nullable: false},
		"text_col":          {formatted: "VARCHAR(12)", affinity: execution.TypeAffinityText, nullable: true, defaultV: stringPtr("''")},
		"blob_col":          {formatted: "BLOB", affinity: execution.TypeAffinityBlob, nullable: true},
		"real_col":          {formatted: "REAL", affinity: execution.TypeAffinityReal, nullable: true},
		"numeric_col":       {formatted: "DECIMAL(10,2)", affinity: execution.TypeAffinityNumeric, nullable: true},
		"empty_col":         {formatted: "", affinity: execution.TypeAffinityBlob, nullable: true},
		"generated_virtual": {formatted: "TEXT", affinity: execution.TypeAffinityText, nullable: true, generated: "virtual"},
		"generated_stored":  {formatted: "INT", affinity: execution.TypeAffinityInteger, nullable: true, generated: "stored"},
	}
	for index, column := range all.Columns {
		expected, ok := want[column.Name]
		if !ok {
			t.Fatalf("unexpected column %q", column.Name)
		}
		if column.OrdinalPosition != index+1 {
			t.Errorf("%s ordinal = %d, want %d", column.Name, column.OrdinalPosition, index+1)
		}
		if column.FormattedType != expected.formatted || column.Type.Affinity != expected.affinity || column.Nullable != expected.nullable {
			t.Errorf("%s metadata = %#v, want declaration=%q affinity=%q nullable=%v", column.Name, column, expected.formatted, expected.affinity, expected.nullable)
		}
		if column.Type.Schema != "" || column.Type.Name != "" || column.Type.Category != execution.TypeCategoryDynamic || column.Type.Length != nil || column.Type.Precision != nil || column.Type.Scale != nil || column.Type.TemporalPrecision != nil || column.Type.IsArray || column.Type.ElementType != nil || column.Type.DomainBaseType != nil {
			t.Errorf("%s dynamic type metadata = %#v, want only category and affinity", column.Name, column.Type)
		}
		if !reflect.DeepEqual(column.DefaultExpression, expected.defaultV) {
			t.Errorf("%s default = %#v, want %#v", column.Name, column.DefaultExpression, expected.defaultV)
		}
		if expected.generated == "" {
			if column.Generated != nil || column.Identity != nil || column.Description != nil {
				t.Errorf("%s optional metadata = %#v, want absent", column.Name, column)
			}
		} else if column.Generated == nil || column.Generated.Kind != expected.generated || column.Generated.Expression != "" {
			t.Errorf("%s generated = %#v, want %q without expression", column.Name, column.Generated, expected.generated)
		}
	}

	filtered, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID: "columns",
		Schema:   "main",
		Table:    "column_subject",
		Search:   "GENERATED",
		Limit:    20,
	})
	if err != nil {
		t.Fatalf("ListColumns(filtered) error = %v", err)
	}
	if got := []string{filtered.Columns[0].Name, filtered.Columns[1].Name}; !reflect.DeepEqual(got, []string{"generated_virtual", "generated_stored"}) {
		t.Fatalf("filtered columns = %#v, want generated columns", got)
	}

	after, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID:     "columns",
		Schema:       "main",
		Table:        "column_subject",
		AfterOrdinal: 4,
		Limit:        20,
	})
	if err != nil {
		t.Fatalf("ListColumns(after) error = %v", err)
	}
	if len(after.Columns) != 4 || after.Columns[0].OrdinalPosition != 5 {
		t.Fatalf("after columns = %#v, want four columns starting at ordinal five", after.Columns)
	}
}

func TestDiscovererListColumnsMapsViewVirtualAndHostileNames(t *testing.T) {
	t.Parallel()

	discoverer := newColumnDiscoverer(t)
	for _, test := range []struct {
		name  string
		table string
		kind  execution.RelationKind
		want  []string
	}{
		{name: "view", table: "column_view", kind: execution.RelationKindView, want: []string{"id", "text_col"}},
		{name: "virtual", table: "column_virtual", kind: execution.RelationKindVirtualTable, want: []string{"content"}},
		{name: "hostile", table: `100%;_column"quoted`, kind: execution.RelationKindTable, want: []string{"value"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			page, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
				SourceID: "columns",
				Schema:   "main",
				Table:    test.table,
				Limit:    20,
			})
			if err != nil {
				t.Fatalf("ListColumns(%q) error = %v", test.table, err)
			}
			if page.RelationKind != test.kind {
				t.Fatalf("ListColumns(%q).RelationKind = %q, want %q", test.table, page.RelationKind, test.kind)
			}
			got := make([]string, 0, len(page.Columns))
			for _, column := range page.Columns {
				got = append(got, column.Name)
				if strings.Contains(column.Name, "hidden") {
					t.Fatalf("hidden column leaked: %#v", column)
				}
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ListColumns(%q) names = %#v, want %#v", test.table, got, test.want)
			}
		})
	}
}

func TestDiscovererListColumnsRejectsUnknownOrExcludedRelations(t *testing.T) {
	t.Parallel()

	discoverer := newColumnDiscoverer(t)
	for _, test := range []struct {
		name  string
		table string
		want  error
	}{
		{name: "missing", table: "missing", want: execution.ErrRelationNotFound},
		{name: "internal", table: "sqlite_sequence", want: execution.ErrRelationNotFound},
		{name: "shadow", table: "column_virtual_data", want: execution.ErrRelationNotFound},
		{name: "index", table: "column_subject_text_idx", want: execution.ErrRelationNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
				SourceID: "columns",
				Schema:   "main",
				Table:    test.table,
				Limit:    1,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("ListColumns(%q) error = %v, want %v", test.table, err, test.want)
			}
		})
	}
}

func newColumnDiscoverer(t *testing.T) *Discoverer {
	t.Helper()

	path := createColumnFixture(t)
	runtime, err := newRuntime(&fixturePreparer{path: path}, openColumnFixtureConnection)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	discoverer, err := NewDiscoverer(runtime)
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v", err)
	}
	return discoverer
}

func openColumnFixtureConnection(ctx context.Context, path string, mode accessMode) (rawConnection, error) {
	connection, err := openPhysicalConnection(ctx, path, mode)
	if err != nil {
		return nil, err
	}
	physical, ok := connection.(*physicalConnection)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("sqlite: column fixture opener received unexpected connection")
	}
	if err := statementext.Register(physical.conn); err != nil {
		return nil, errors.Join(err, connection.Close())
	}
	return connection, nil
}

func createColumnFixture(t *testing.T) string {
	t.Helper()

	path := t.TempDir() + "/columns.db"
	conn, err := sqlite3.OpenFlags(path, sqlite3.OPEN_READWRITE|sqlite3.OPEN_CREATE|sqlite3.OPEN_URI)
	if err != nil {
		t.Fatalf("OpenFlags(columns) error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := statementext.Register(conn); err != nil {
		t.Fatalf("Register(statement) error = %v", err)
	}

	statements := []string{
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE "column_subject" (
			id INTEGER NOT NULL,
			text_col VARCHAR(12) DEFAULT '',
			blob_col BLOB,
			real_col REAL,
			numeric_col DECIMAL(10,2),
			empty_col,
			generated_virtual TEXT GENERATED ALWAYS AS (text_col || '!') VIRTUAL,
			generated_stored INT GENERATED ALWAYS AS (id + 1) STORED
		)`,
		`CREATE VIEW "column_view" AS SELECT id, text_col FROM column_subject`,
		`CREATE VIRTUAL TABLE "column_virtual" USING statement((SELECT 'value' AS content))`,
	}
	statements = append(statements, fmt.Sprintf("CREATE TABLE %s (value TEXT)", quoteFixtureIdentifier(`100%;_column"quoted`)))
	statements = append(statements, `CREATE INDEX "column_subject_text_idx" ON "column_subject" (text_col)`)
	for _, statement := range statements {
		if err := conn.Exec(statement); err != nil {
			t.Fatalf("fixture Exec(%q) error = %v", statement, err)
		}
	}

	return path
}

func stringPtr(value string) *string {
	return &value
}
