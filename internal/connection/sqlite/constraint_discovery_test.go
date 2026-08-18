package sqlite

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/adamraziv/dataporch/internal/execution"
	sqlite3 "github.com/ncruces/go-sqlite3"
	statementext "github.com/ncruces/go-sqlite3/ext/statement"
)

func TestDiscovererListConstraintsMapsStructuralMetadata(t *testing.T) {
	t.Parallel()

	discoverer := newConstraintDiscoverer(t)

	page, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID: "constraints",
		Schema:   "main",
		Table:    "constraint_child",
		Limit:    50,
	})
	if err != nil {
		t.Fatalf("ListColumns() error = %v", err)
	}

	if len(page.Columns) == 0 || page.RelationKind != execution.RelationKindTable {
		t.Fatalf("ListColumns() = %#v, want child table columns", page)
	}

	if len(page.Constraints) != 6 {
		t.Fatalf("constraints = %#v, want primary, unique, index, and three foreign keys", page.Constraints)
	}

	var (
		primary execution.Constraint
		unique  execution.Constraint
		index   execution.Constraint
		foreign []execution.Constraint
	)

	for _, constraint := range page.Constraints {
		if constraint.Name != "" || constraint.Deferrable || constraint.InitiallyDeferred || constraint.Validated || constraint.NullsNotDistinct != nil || constraint.CheckExpression != nil {
			t.Errorf("constraint metadata = %#v, want names and unavailable flags omitted", constraint)
		}

		switch constraint.Kind {
		case "primary_key":
			primary = constraint
		case "unique":
			if len(constraint.Columns) == 2 {
				unique = constraint
			} else {
				index = constraint
			}
		case "foreign_key":
			foreign = append(foreign, constraint)
		default:
			t.Errorf("unexpected constraint kind %q", constraint.Kind)
		}
	}

	if !reflect.DeepEqual(primary.Columns, []string{"parent_id", "parent_code"}) {
		t.Fatalf("primary key columns = %#v, want composite order", primary.Columns)
	}

	if !reflect.DeepEqual(unique.Columns, []string{"first_unique", "second_unique"}) {
		t.Fatalf("declared unique columns = %#v, want composite order", unique.Columns)
	}

	if !reflect.DeepEqual(index.Columns, []string{"extra_unique"}) {
		t.Fatalf("full unique index columns = %#v, want extra_unique", index.Columns)
	}

	sort.Slice(foreign, func(i, j int) bool { return foreign[i].Columns[0] < foreign[j].Columns[0] })

	if len(foreign) != 3 {
		t.Fatalf("foreign constraints = %#v, want three", foreign)
	}

	if got := foreign[0]; got.Referenced == nil || !reflect.DeepEqual(got.Columns, []string{"implicit_parent"}) || got.Referenced.Table != "constraint_parent" || len(got.Referenced.Columns) != 0 || got.MatchType != "simple" || got.UpdateAction != "no_action" || got.DeleteAction != "cascade" {
		t.Fatalf("implicit foreign key = %#v, want empty referenced columns and normalized actions", got)
	}

	if got := foreign[1]; got.Referenced == nil || !reflect.DeepEqual(got.Columns, []string{"parent_code"}) || !reflect.DeepEqual(got.Referenced.Columns, []string{"code"}) || got.MatchType != "simple" || got.UpdateAction != "restrict" || got.DeleteAction != "set_default" {
		t.Fatalf("explicit foreign key = %#v, want parent code and normalized actions", got)
	}

	if got := foreign[2]; got.Referenced == nil || !reflect.DeepEqual(got.Columns, []string{"parent_id"}) || !reflect.DeepEqual(got.Referenced.Columns, []string{"id"}) || got.MatchType != "simple" || got.UpdateAction != "cascade" || got.DeleteAction != "set_null" {
		t.Fatalf("MATCH FULL foreign key = %#v, want explicit parent and normalized actions", got)
	}
}

func TestDiscovererListConstraintsOnlyOnFirstColumnPage(t *testing.T) {
	t.Parallel()

	discoverer := newConstraintDiscoverer(t)

	page, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID:     "constraints",
		Schema:       "main",
		Table:        "constraint_child",
		Limit:        2,
		AfterOrdinal: 2,
	})
	if err != nil {
		t.Fatalf("ListColumns(after) error = %v", err)
	}

	if page.Constraints == nil || len(page.Constraints) != 0 {
		t.Fatalf("later-page constraints = %#v, want initialized empty slice", page.Constraints)
	}
}

func TestForeignKeyMetadataNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code string
		want string
	}{
		{name: "none", code: "NONE", want: "simple"},
		{name: "simple", code: "SIMPLE", want: "simple"},
		{name: "partial", code: "PARTIAL", want: "partial"},
		{name: "full", code: "FULL", want: "full"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got, err := foreignKeyMatch(test.code); err != nil || got != test.want {
				t.Fatalf("foreignKeyMatch(%q) = %q, %v; want %q", test.code, got, err, test.want)
			}
		})
	}

	if _, err := foreignKeyMatch("UNKNOWN"); !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("foreignKeyMatch(UNKNOWN) error = %v, want internal", err)
	}
}

func newConstraintDiscoverer(t *testing.T) *Discoverer {
	t.Helper()

	path := createConstraintFixture(t)

	runtime, err := newRuntime(&fixturePreparer{path: path}, openConstraintFixtureConnection)
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}

	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	discoverer, err := NewDiscoverer(runtime)
	if err != nil {
		t.Fatalf("NewDiscoverer() error = %v", err)
	}

	return discoverer
}

func openConstraintFixtureConnection(ctx context.Context, path string, mode accessMode) (rawConnection, error) {
	connection, err := openPhysicalConnection(ctx, path, mode)
	if err != nil {
		return nil, err
	}

	physical, ok := connection.(*physicalConnection)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("sqlite: constraint fixture opener received unexpected connection")
	}

	if err := statementext.Register(physical.conn); err != nil {
		return nil, errors.Join(err, connection.Close())
	}

	return connection, nil
}

func createConstraintFixture(t *testing.T) string {
	t.Helper()

	path := t.TempDir() + "/constraints.db"

	conn, err := sqlite3.OpenFlags(path, sqlite3.OPEN_READWRITE|sqlite3.OPEN_CREATE|sqlite3.OPEN_URI)
	if err != nil {
		t.Fatalf("OpenFlags(constraints) error = %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	if err := statementext.Register(conn); err != nil {
		t.Fatalf("Register(statement) error = %v", err)
	}

	statements := []string{
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE "constraint_parent" (
			id INTEGER PRIMARY KEY,
			code TEXT UNIQUE
		)`,
		`CREATE TABLE "constraint_child" (
			parent_id INTEGER,
			parent_code TEXT,
			implicit_parent INTEGER,
			first_unique TEXT,
			second_unique TEXT,
			extra_unique TEXT,
			PRIMARY KEY (parent_id, parent_code),
			UNIQUE (first_unique, second_unique),
			FOREIGN KEY (parent_id) REFERENCES constraint_parent(id) MATCH FULL ON UPDATE CASCADE ON DELETE SET NULL,
			FOREIGN KEY (parent_code) REFERENCES constraint_parent(code) ON UPDATE RESTRICT ON DELETE SET DEFAULT,
			FOREIGN KEY (implicit_parent) REFERENCES constraint_parent ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX "100%;_unique_idx" ON "constraint_child" (extra_unique)`,
		`CREATE UNIQUE INDEX "partial_unique_idx" ON "constraint_child" (extra_unique) WHERE extra_unique IS NOT NULL`,
		`CREATE UNIQUE INDEX "expression_unique_idx" ON "constraint_child" (lower(extra_unique))`,
	}
	for _, statement := range statements {
		if err := conn.Exec(statement); err != nil {
			t.Fatalf("fixture Exec(%q) error = %v", statement, err)
		}
	}

	return path
}
