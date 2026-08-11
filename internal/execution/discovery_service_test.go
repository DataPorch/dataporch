package execution

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/adamraziv/dataporch/internal/access"
	"github.com/adamraziv/dataporch/internal/connection"
)

func TestNewRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()

	validSources := &sourceRegistryStub{}
	validAuthorizer := &recordingAuthorizer{}
	validDiscoverer := &panicDiscoverer{kind: "postgres"}
	var typedNilSources *sourceRegistryStub
	var typedNilAuthorizer *recordingAuthorizer
	var typedNilDiscoverer *panicDiscoverer

	tests := []struct {
		name string
		deps Dependencies
	}{
		{name: "missing sources", deps: Dependencies{Authorizer: validAuthorizer, MaxLimit: 10}},
		{name: "typed nil sources", deps: Dependencies{Sources: typedNilSources, Authorizer: validAuthorizer, MaxLimit: 10}},
		{name: "missing authorizer", deps: Dependencies{Sources: validSources, MaxLimit: 10}},
		{name: "typed nil authorizer", deps: Dependencies{Sources: validSources, Authorizer: typedNilAuthorizer, MaxLimit: 10}},
		{name: "invalid maximum", deps: Dependencies{Sources: validSources, Authorizer: validAuthorizer}},
		{name: "nil discoverer", deps: Dependencies{Sources: validSources, Authorizer: validAuthorizer, MaxLimit: 10, RelationalDiscoverers: []RelationalDiscoverer{nil}}},
		{name: "typed nil discoverer", deps: Dependencies{Sources: validSources, Authorizer: validAuthorizer, MaxLimit: 10, RelationalDiscoverers: []RelationalDiscoverer{typedNilDiscoverer}}},
		{name: "empty kind", deps: Dependencies{Sources: validSources, Authorizer: validAuthorizer, MaxLimit: 10, RelationalDiscoverers: []RelationalDiscoverer{&panicDiscoverer{}}}},
		{name: "duplicate kind", deps: Dependencies{Sources: validSources, Authorizer: validAuthorizer, MaxLimit: 10, RelationalDiscoverers: []RelationalDiscoverer{validDiscoverer, &panicDiscoverer{kind: "postgres"}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.deps); err == nil {
				t.Fatal("New() error = nil, want non-nil")
			}
		})
	}
}

func TestServiceListDataSources(t *testing.T) {
	t.Parallel()

	authorizer := &recordingAuthorizer{}
	sources := &sourceRegistryStub{definitions: []connection.Definition{
		{ID: "zeta", Kind: "unknown", Settings: map[string]string{"host": "z"}},
		{ID: "alpha", Kind: "postgres", Settings: map[string]string{"host": "a"}},
		{ID: "beta", Kind: "postgres"},
	}}
	discoverer := &panicDiscoverer{kind: "postgres"}
	service, err := New(Dependencies{
		Sources:               sources,
		Authorizer:            authorizer,
		MaxLimit:              2,
		RelationalDiscoverers: []RelationalDiscoverer{discoverer},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := service.ListDataSources(t.Context(), ListDataSourcesRequest{})
	if err != nil {
		t.Fatalf("ListDataSources() error = %v", err)
	}
	if got := []connection.ID{result.Sources[0].ID, result.Sources[1].ID}; !reflect.DeepEqual(got, []connection.ID{"alpha", "beta"}) {
		t.Fatalf("sources = %v, want [alpha beta]", got)
	}
	if result.NextCursor == "" {
		t.Fatal("NextCursor = empty, want continuation")
	}
	if !reflect.DeepEqual(result.Sources[0].Capabilities, []Capability{CapabilityRelationalDatabase}) {
		t.Fatalf("capabilities = %v, want relational_database", result.Sources[0].Capabilities)
	}
	if result.Sources[1].Capabilities == nil {
		t.Fatal("unsupported source capabilities = nil, want initialized empty slice")
	}
	if got := authorizer.actions; !reflect.DeepEqual(got, []access.Action{access.ActionListDataSources}) {
		t.Fatalf("authorization actions = %v, want list_data_sources", got)
	}
	if discoverer.listCalls != 0 || discoverer.kindCalls != 1 {
		t.Fatalf("discoverer calls = kind %d/list %d, want kind 1/list 0", discoverer.kindCalls, discoverer.listCalls)
	}

	result.Sources[0].Capabilities[0] = "mutated"
	next, err := service.ListDataSources(t.Context(), ListDataSourcesRequest{Search: "ALP"})
	if err != nil {
		t.Fatalf("ListDataSources(search) error = %v", err)
	}
	if len(next.Sources) != 1 || next.Sources[0].ID != "alpha" {
		t.Fatalf("search sources = %#v, want alpha", next.Sources)
	}
	if next.Sources[0].Capabilities[0] != CapabilityRelationalDatabase {
		t.Fatal("output mutation changed service capability state")
	}
}

func TestServiceListDataSourcesCursorAndLimits(t *testing.T) {
	t.Parallel()

	sources := &sourceRegistryStub{definitions: []connection.Definition{
		{ID: "alpha", Kind: "postgres"},
		{ID: "beta", Kind: "postgres"},
		{ID: "gamma", Kind: "postgres"},
	}}
	service, err := New(Dependencies{Sources: sources, Authorizer: &recordingAuthorizer{}, MaxLimit: 2})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	limit := 1
	first, err := service.ListDataSources(t.Context(), ListDataSourcesRequest{Limit: &limit})
	if err != nil {
		t.Fatalf("first ListDataSources() error = %v", err)
	}
	if len(first.Sources) != 1 || first.Sources[0].ID != "alpha" || first.NextCursor == "" {
		t.Fatalf("first page = %#v, want alpha with cursor", first)
	}
	second, err := service.ListDataSources(t.Context(), ListDataSourcesRequest{Limit: &limit, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second ListDataSources() error = %v", err)
	}
	if len(second.Sources) != 1 || second.Sources[0].ID != "beta" {
		t.Fatalf("second page = %#v, want beta", second)
	}
	changedLimit := 2
	if _, err := service.ListDataSources(t.Context(), ListDataSourcesRequest{Limit: &changedLimit, Cursor: first.NextCursor}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("changed-limit cursor error = %v, want ErrInvalidCursor", err)
	}
	zero := 0
	if _, err := service.ListDataSources(t.Context(), ListDataSourcesRequest{Limit: &zero}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("zero limit error = %v, want ErrInvalidRequest", err)
	}
}

func TestServiceListRelationalOperations(t *testing.T) {
	t.Parallel()

	authorizer := &recordingAuthorizer{}
	discoverer := &recordingDiscoverer{
		kind: "postgres",
		schemasPage: SchemaDiscoveryPage{
			Schemas: []Schema{{Name: "public"}},
			HasMore: true,
		},
		tablesPage: TableDiscoveryPage{
			Tables: []Table{{Name: "Customers", Kind: RelationKindTable}},
		},
		columnsPage: ColumnDiscoveryPage{
			Columns: []Column{{
				Name:            "id",
				OrdinalPosition: 1,
				Type: DataType{
					ElementType: &TypeReference{Schema: "pg_catalog", Name: "text"},
				},
				DefaultExpression: stringPointer("nextval('seq')"),
			}},
			RelationKind: RelationKindTable,
			Constraints: []Constraint{{
				Name:    "customers_pkey",
				Kind:    "primary_key",
				Columns: []string{"id"},
			}},
		},
	}
	service, err := New(Dependencies{
		Sources:               &sourceRegistryStub{definitions: []connection.Definition{{ID: "analytics", Kind: "postgres"}}},
		Authorizer:            authorizer,
		MaxLimit:              10,
		RelationalDiscoverers: []RelationalDiscoverer{discoverer},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	schemaResult, err := service.ListRelationalSchemas(t.Context(), ListRelationalSchemasRequest{SourceID: "analytics", IncludeDescriptions: true, Limit: intPointer(2)})
	if err != nil {
		t.Fatalf("ListRelationalSchemas() error = %v", err)
	}
	if schemaResult.SourceID != "analytics" || len(schemaResult.Schemas) != 1 || schemaResult.NextCursor == "" {
		t.Fatalf("schema result = %#v, want source, one schema, cursor", schemaResult)
	}
	if discoverer.schemasRequest.SourceID != "analytics" || discoverer.schemasRequest.Limit != 2 || !discoverer.schemasRequest.IncludeDescriptions {
		t.Fatalf("schema request = %#v", discoverer.schemasRequest)
	}

	tableResult, err := service.ListRelationalTables(t.Context(), ListRelationalTablesRequest{
		SourceID: "analytics",
		Schema:   "Sales Data",
		Search:   `%_*."[x]\\`,
	})
	if err != nil {
		t.Fatalf("ListRelationalTables() error = %v", err)
	}
	if tableResult.Schema != "Sales Data" || len(tableResult.Tables) != 1 {
		t.Fatalf("table result = %#v", tableResult)
	}
	if discoverer.tablesRequest.Schema != "Sales Data" || discoverer.tablesRequest.Search != `%_*."[x]\\` {
		t.Fatalf("table request = %#v", discoverer.tablesRequest)
	}

	columnResult, err := service.ListRelationalColumns(t.Context(), ListRelationalColumnsRequest{SourceID: "analytics", Schema: "Sales Data", Table: "Customers"})
	if err != nil {
		t.Fatalf("ListRelationalColumns() error = %v", err)
	}
	columnResult.Columns[0].Type.ElementType.Name = "mutated"
	columnResult.Constraints[0].Columns[0] = "mutated"
	if discoverer.columnsPage.Columns[0].Type.ElementType.Name != "text" || discoverer.columnsPage.Constraints[0].Columns[0] != "id" {
		t.Fatal("column result mutation changed discoverer page")
	}
	if !reflect.DeepEqual(authorizer.actions, []access.Action{
		access.ActionListRelationalSchemas,
		access.ActionListRelationalTables,
		access.ActionListRelationalColumns,
	}) {
		t.Fatalf("authorization actions = %v", authorizer.actions)
	}
}

func TestServiceRelationalValidationAndRouting(t *testing.T) {
	t.Parallel()

	authorizer := &recordingAuthorizer{}
	discoverer := &recordingDiscoverer{kind: "postgres", tablesPage: TableDiscoveryPage{Tables: []Table{{Name: "orders"}}}}
	service, err := New(Dependencies{
		Sources:               &sourceRegistryStub{definitions: []connection.Definition{{ID: "analytics", Kind: "postgres"}, {ID: "memory", Kind: "memory"}}},
		Authorizer:            authorizer,
		MaxLimit:              10,
		RelationalDiscoverers: []RelationalDiscoverer{discoverer},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for _, test := range []struct {
		name string
		call func() error
		want error
	}{
		{name: "missing source", call: func() error {
			_, err := service.ListRelationalTables(t.Context(), ListRelationalTablesRequest{SourceID: "missing", Schema: "public"})
			return err
		}, want: ErrSourceNotFound},
		{name: "unsupported source", call: func() error {
			_, err := service.ListRelationalTables(t.Context(), ListRelationalTablesRequest{SourceID: "memory", Schema: "public"})
			return err
		}, want: ErrUnsupportedSourceCapability},
		{name: "empty schema", call: func() error {
			_, err := service.ListRelationalTables(t.Context(), ListRelationalTablesRequest{SourceID: "analytics"})
			return err
		}, want: ErrInvalidRequest},
		{name: "control schema", call: func() error {
			_, err := service.ListRelationalTables(t.Context(), ListRelationalTablesRequest{SourceID: "analytics", Schema: "public\n"})
			return err
		}, want: ErrInvalidRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	overbound := &recordingDiscoverer{kind: "postgres", tablesPage: TableDiscoveryPage{Tables: make([]Table, 2)}}
	service, err = New(Dependencies{Sources: &sourceRegistryStub{definitions: []connection.Definition{{ID: "analytics", Kind: "postgres"}}}, Authorizer: &recordingAuthorizer{}, MaxLimit: 1, RelationalDiscoverers: []RelationalDiscoverer{overbound}})
	if err != nil {
		t.Fatalf("New(overbound) error = %v", err)
	}
	if _, err := service.ListRelationalTables(t.Context(), ListRelationalTablesRequest{SourceID: "analytics", Schema: "public"}); !errors.Is(err, ErrInternal) {
		t.Fatalf("overbound error = %v, want ErrInternal", err)
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		category  ErrorCategory
		retryable bool
	}{
		{name: "invalid request", err: ErrInvalidRequest, category: ErrorCategoryInvalidRequest},
		{name: "invalid cursor", err: ErrInvalidCursor, category: ErrorCategoryInvalidCursor},
		{name: "database unavailable", err: ErrDatabaseUnavailable, category: ErrorCategoryDatabaseUnavailable, retryable: true},
		{name: "timeout", err: ErrQueryTimeout, category: ErrorCategoryQueryTimeout, retryable: true},
		{name: "cancelled", err: context.Canceled, category: ErrorCategoryCancelled},
		{name: "unknown", err: errors.New("sensitive database message"), category: ErrorCategoryInternal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := Classify(test.err)
			if failure.Category != test.category || failure.Retryable != test.retryable {
				t.Fatalf("Classify() = %#v, want category %q retryable %t", failure, test.category, test.retryable)
			}
			if strings.Contains(failure.Message, "sensitive") {
				t.Fatal("Classify() exposed raw error")
			}
		})
	}
}

type sourceRegistryStub struct {
	definitions []connection.Definition
}

func (s *sourceRegistryStub) List() []connection.Definition {
	result := make([]connection.Definition, len(s.definitions))
	for index, definition := range s.definitions {
		result[index] = definition.Clone()
	}
	return result
}

func (s *sourceRegistryStub) Lookup(id connection.ID) (connection.Definition, error) {
	for _, definition := range s.definitions {
		if definition.ID == id {
			return definition.Clone(), nil
		}
	}
	return connection.Definition{}, connection.ErrDatabaseNotFound
}

type recordingAuthorizer struct {
	actions []access.Action
	err     error
}

func (a *recordingAuthorizer) Authorize(_ context.Context, action access.Action) error {
	a.actions = append(a.actions, action)
	return a.err
}

type panicDiscoverer struct {
	kind      connection.Kind
	kindCalls int
	listCalls int
}

type recordingDiscoverer struct {
	kind           connection.Kind
	schemasRequest SchemaDiscoveryRequest
	tablesRequest  TableDiscoveryRequest
	columnsRequest ColumnDiscoveryRequest
	schemasPage    SchemaDiscoveryPage
	tablesPage     TableDiscoveryPage
	columnsPage    ColumnDiscoveryPage
}

func (d *recordingDiscoverer) Kind() connection.Kind { return d.kind }

func (d *recordingDiscoverer) ListSchemas(_ context.Context, request SchemaDiscoveryRequest) (SchemaDiscoveryPage, error) {
	d.schemasRequest = request
	return d.schemasPage, nil
}

func (d *recordingDiscoverer) ListTables(_ context.Context, request TableDiscoveryRequest) (TableDiscoveryPage, error) {
	d.tablesRequest = request
	return d.tablesPage, nil
}

func (d *recordingDiscoverer) ListColumns(_ context.Context, request ColumnDiscoveryRequest) (ColumnDiscoveryPage, error) {
	d.columnsRequest = request
	return d.columnsPage, nil
}

func intPointer(value int) *int { return &value }

func stringPointer(value string) *string { return &value }

func (d *panicDiscoverer) Kind() connection.Kind {
	d.kindCalls++
	return d.kind
}

func (d *panicDiscoverer) ListSchemas(context.Context, SchemaDiscoveryRequest) (SchemaDiscoveryPage, error) {
	d.listCalls++
	panic("ListSchemas should not be called by data-source listing")
}

func (d *panicDiscoverer) ListTables(context.Context, TableDiscoveryRequest) (TableDiscoveryPage, error) {
	d.listCalls++
	panic("ListTables should not be called by data-source listing")
}

func (d *panicDiscoverer) ListColumns(context.Context, ColumnDiscoveryRequest) (ColumnDiscoveryPage, error) {
	d.listCalls++
	panic("ListColumns should not be called by data-source listing")
}
