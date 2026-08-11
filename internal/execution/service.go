package execution

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/adamraziv/dataporch/internal/access"
	"github.com/adamraziv/dataporch/internal/catalog"
)

var (
	ErrInvalidLimit = errors.New("execution: invalid resource limit")

	errCatalogRequired    = errors.New("execution: resource catalog is required")
	errAuthorizerRequired = errors.New("execution: authorizer is required")
	errContextRequired    = errors.New("execution: context is required")
)

type ResourceCatalog interface {
	ListResources(context.Context, int) ([]catalog.Resource, error)
}

type Authorizer interface {
	Authorize(context.Context, access.Action) error
}

type ResourceService struct {
	catalog    ResourceCatalog
	authorizer Authorizer
	maxLimit   int
}

func NewResourceService(
	resourceCatalog ResourceCatalog,
	authorizer Authorizer,
	maxLimit int,
) (*ResourceService, error) {
	if resourceCatalog == nil {
		return nil, errCatalogRequired
	}

	if authorizer == nil {
		return nil, errAuthorizerRequired
	}

	if maxLimit <= 0 {
		return nil, fmt.Errorf("%w: maximum must be positive", ErrInvalidLimit)
	}

	return &ResourceService{
		catalog:    resourceCatalog,
		authorizer: authorizer,
		maxLimit:   maxLimit,
	}, nil
}

func (s *ResourceService) ListResources(
	ctx context.Context,
	limit int,
) ([]catalog.Resource, error) {
	if ctx == nil {
		return nil, errContextRequired
	}

	if limit <= 0 || limit > s.maxLimit {
		return nil, fmt.Errorf(
			"%w: must be between 1 and %d",
			ErrInvalidLimit,
			s.maxLimit,
		)
	}

	if err := s.authorizer.Authorize(ctx, access.ActionListResources); err != nil {
		return nil, fmt.Errorf("authorizing resource listing: %w", err)
	}

	resources, err := s.catalog.ListResources(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("listing catalog resources: %w", err)
	}

	return slices.Clone(resources), nil
}
