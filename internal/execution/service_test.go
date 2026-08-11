package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/adamraziv/dataporch/internal/access"
	"github.com/adamraziv/dataporch/internal/catalog"
)

type catalogStub struct {
	resources []catalog.Resource
}

func (s catalogStub) ListResources(
	_ context.Context,
	limit int,
) ([]catalog.Resource, error) {
	return s.resources[:min(limit, len(s.resources))], nil
}

type authorizerStub struct {
	err error
}

func (s authorizerStub) Authorize(context.Context, access.Action) error {
	return s.err
}

func TestNewResourceService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		catalog    ResourceCatalog
		authorizer Authorizer
		maxLimit   int
	}{
		{name: "missing catalog", authorizer: authorizerStub{}, maxLimit: 10},
		{name: "missing authorizer", catalog: catalogStub{}, maxLimit: 10},
		{name: "invalid maximum", catalog: catalogStub{}, authorizer: authorizerStub{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NewResourceService(tt.catalog, tt.authorizer, tt.maxLimit); err == nil {
				t.Fatal("NewResourceService() error = nil, want non-nil")
			}
		})
	}
}

func TestService_ListResources(t *testing.T) {
	t.Parallel()

	service, err := NewResourceService(
		catalogStub{resources: []catalog.Resource{
			{URI: "memory://customers", Name: "Customers", Kind: "table"},
		}},
		authorizerStub{},
		10,
	)
	if err != nil {
		t.Fatalf("NewResourceService() error = %v", err)
	}

	resources, err := service.ListResources(t.Context(), 1)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}

	if len(resources) != 1 {
		t.Fatalf("ListResources() returned %d resources, want 1", len(resources))
	}
}

func TestService_ListResourcesRejectsInvalidLimit(t *testing.T) {
	t.Parallel()

	service, err := NewResourceService(catalogStub{}, authorizerStub{}, 10)
	if err != nil {
		t.Fatalf("NewResourceService() error = %v", err)
	}

	_, err = service.ListResources(t.Context(), 11)
	if !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("ListResources() error = %v, want ErrInvalidLimit", err)
	}
}

func TestService_ListResourcesRejectsDeniedAccess(t *testing.T) {
	t.Parallel()

	service, err := NewResourceService(
		catalogStub{},
		authorizerStub{err: errors.New("access denied")},
		10,
	)
	if err != nil {
		t.Fatalf("NewResourceService() error = %v", err)
	}

	if _, err := service.ListResources(t.Context(), 1); err == nil {
		t.Fatal("ListResources() error = nil, want authorization error")
	}
}
