package memory

import (
	"testing"

	"github.com/adamraziv/dataporch/internal/catalog"
)

const tableKind = "table"

func TestNew(t *testing.T) {
	t.Parallel()

	_, err := New([]catalog.Resource{
		{URI: "memory://customers", Name: "Customers", Kind: tableKind},
		{URI: "memory://customers", Name: "Customers copy", Kind: tableKind},
	})
	if err == nil {
		t.Fatal("New() error = nil, want duplicate resource error")
	}
}

func TestCatalog_ListResources(t *testing.T) {
	t.Parallel()

	connector, err := New([]catalog.Resource{
		{URI: "memory://customers", Name: "Customers", Kind: tableKind},
		{URI: "memory://orders", Name: "Orders", Kind: tableKind},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	resources, err := connector.ListResources(t.Context(), 1)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}

	if len(resources) != 1 {
		t.Fatalf("ListResources() returned %d resources, want 1", len(resources))
	}

	resources[0].Name = "mutated"

	again, err := connector.ListResources(t.Context(), 1)
	if err != nil {
		t.Fatalf("ListResources() second call error = %v", err)
	}

	if again[0].Name == "mutated" {
		t.Fatal("ListResources() returned mutable internal state")
	}
}
