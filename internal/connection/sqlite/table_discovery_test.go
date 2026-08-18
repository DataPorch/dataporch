package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	sqlite3 "github.com/ncruces/go-sqlite3"
	statementext "github.com/ncruces/go-sqlite3/ext/statement"
)

func TestDiscovererListSchemas(t *testing.T) {
	t.Parallel()

	discoverer := newFixtureDiscoverer(t)

	page, err := discoverer.ListSchemas(t.Context(), execution.SchemaDiscoveryRequest{
		SourceID: "fixture",
		Limit:    1,
	})
	if err != nil {
		t.Fatalf("ListSchemas() error = %v", err)
	}

	if !reflect.DeepEqual(page.Schemas, []execution.Schema{{Name: "main"}}) || page.HasMore {
		t.Fatalf("ListSchemas() = %#v, want only main without more", page)
	}

	for _, test := range []struct {
		name      string
		search    string
		afterName string
		want      []execution.Schema
	}{
		{name: "case insensitive match", search: "MA", want: []execution.Schema{{Name: "main"}}},
		{name: "search miss", search: "temp", want: []execution.Schema{}},
		{name: "after main", afterName: "main", want: []execution.Schema{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			page, err := discoverer.ListSchemas(t.Context(), execution.SchemaDiscoveryRequest{
				SourceID:  "fixture",
				Search:    test.search,
				AfterName: test.afterName,
				Limit:     2,
			})
			if err != nil {
				t.Fatalf("ListSchemas() error = %v", err)
			}

			if !reflect.DeepEqual(page.Schemas, test.want) || page.HasMore {
				t.Fatalf("ListSchemas() = %#v, want %#v without more", page, test.want)
			}
		})
	}
}

func TestDiscovererListTablesMapsRelationsAndPaginates(t *testing.T) {
	t.Parallel()

	discoverer := newFixtureDiscoverer(t)

	page, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID:            "fixture",
		Schema:              "main",
		IncludeDescriptions: true,
		Limit:               2,
	})
	if err != nil {
		t.Fatalf("ListTables() error = %v", err)
	}

	if len(page.Tables) != 2 || !page.HasMore {
		t.Fatalf("ListTables() = %#v, want two rows and more", page)
	}

	if page.Tables[0].Name != "100%;_name\"quoted" || page.Tables[0].Kind != execution.RelationKindTable {
		t.Fatalf("first table = %#v, want hostile ordinary table", page.Tables[0])
	}

	if page.Tables[0].Description != nil || page.Tables[1].Description != nil {
		t.Fatalf("descriptions = %#v, want nil", page.Tables)
	}

	all, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID: "fixture",
		Schema:   "main",
		Limit:    20,
	})
	if err != nil {
		t.Fatalf("ListTables(all) error = %v", err)
	}

	if all.HasMore {
		t.Fatal("ListTables(all).HasMore = true, want false")
	}

	gotNames := make([]string, 0, len(all.Tables))

	gotKinds := make(map[string]execution.RelationKind, len(all.Tables))
	for _, table := range all.Tables {
		gotNames = append(gotNames, table.Name)

		gotKinds[table.Name] = table.Kind
		if strings.HasPrefix(table.Name, "sqlite_") || strings.HasSuffix(table.Name, "_data") {
			t.Fatalf("internal/shadow table leaked: %#v", table)
		}
	}

	wantNames := append([]string(nil), fixtureRelationNames...)
	sort.Strings(wantNames)

	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("table names = %#v, want %#v", gotNames, wantNames)
	}

	if gotKinds["search_fts"] != execution.RelationKindVirtualTable || gotKinds["orders_view"] != execution.RelationKindView {
		t.Fatalf("relation kinds = %#v, want virtual table and view", gotKinds)
	}

	filtered, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID: "fixture",
		Schema:   "main",
		Search:   "%;_name",
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("ListTables(filtered) error = %v", err)
	}

	if !reflect.DeepEqual(filtered.Tables, []execution.Table{{Name: "100%;_name\"quoted", Kind: execution.RelationKindTable}}) || filtered.HasMore {
		t.Fatalf("filtered tables = %#v, want one hostile-name match", filtered)
	}

	after, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID:  "fixture",
		Schema:    "main",
		AfterName: "100%;_name\"quoted",
		Limit:     20,
	})
	if err != nil {
		t.Fatalf("ListTables(after) error = %v", err)
	}

	if len(after.Tables) == 0 || after.Tables[0].Name == "100%;_name\"quoted" {
		t.Fatalf("after-name page = %#v, want strictly greater names", after)
	}
}

func TestDiscovererRejectsNonMainSchema(t *testing.T) {
	t.Parallel()

	discoverer := newFixtureDiscoverer(t)

	_, err := discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID: "fixture",
		Schema:   "temp",
		Limit:    1,
	})
	if !errors.Is(err, execution.ErrSchemaNotFound) {
		t.Fatalf("ListTables(temp) error = %v, want ErrSchemaNotFound", err)
	}
}

var fixtureRelationNames = []string{"100%;_name\"quoted", "orders", "orders_view", "search_fts"}

func newFixtureDiscoverer(t *testing.T) *Discoverer {
	t.Helper()

	path := createDiscoveryFixture(t)
	preparer := &fixturePreparer{path: path}

	runtime, err := NewRuntime(preparer)
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

func createDiscoveryFixture(t *testing.T) string {
	t.Helper()

	path := t.TempDir() + "/fixture.db"

	conn, err := sqlite3.OpenFlags(path, sqlite3.OPEN_READWRITE|sqlite3.OPEN_CREATE|sqlite3.OPEN_URI)
	if err != nil {
		t.Fatalf("OpenFlags(fixture) error = %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	if err := statementext.Register(conn); err != nil {
		t.Fatalf("Register(statement) error = %v", err)
	}

	statements := []string{
		`CREATE TABLE "orders" (id INTEGER PRIMARY KEY, payload TEXT)`,
		`CREATE VIEW "orders_view" AS SELECT id FROM orders`,
	}
	statements = append(statements, fmt.Sprintf("CREATE TABLE %s (id INTEGER)", quoteFixtureIdentifier("100%;_name\"quoted")))

	statements = append(statements, `CREATE VIRTUAL TABLE "search_fts" USING statement((SELECT 1 AS content))`)
	for _, statement := range statements {
		if err := conn.Exec(statement); err != nil {
			t.Fatalf("fixture Exec(%q) error = %v", statement, err)
		}
	}

	return path
}

type fixturePreparer struct {
	path string
}

func (p *fixturePreparer) Prepare(_ context.Context, id connection.ID) (connection.ResolvedDefinition, error) {
	return connection.ResolvedDefinition{
		ID:   id,
		Kind: Kind,
		Secrets: map[string][]byte{
			secretPath: []byte(p.path),
		},
	}, nil
}

func quoteFixtureIdentifier(value string) string {
	return fmt.Sprintf(`"%s"`, strings.ReplaceAll(value, `"`, `""`))
}
