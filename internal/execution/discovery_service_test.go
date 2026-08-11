package execution

import (
	"context"
	"errors"
	"reflect"
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
