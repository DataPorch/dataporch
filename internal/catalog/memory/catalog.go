package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/adamraziv/dataporch/internal/catalog"
)

var (
	errContextRequired = errors.New("memory connector: context is required")
	errLimitInvalid    = errors.New("memory connector: limit must be positive")
	errDuplicateURI    = errors.New("memory connector: duplicate resource uri")
)

type Catalog struct {
	resources []catalog.Resource
}

func New(resources []catalog.Resource) (*Catalog, error) {
	seen := make(map[string]struct{}, len(resources))
	for index, resource := range resources {
		if err := resource.Validate(); err != nil {
			return nil, fmt.Errorf("validating resource %d: %w", index, err)
		}

		if _, exists := seen[resource.URI]; exists {
			return nil, fmt.Errorf("%w: %q", errDuplicateURI, resource.URI)
		}

		seen[resource.URI] = struct{}{}
	}

	// Preserve nil input so construction retains caller-visible slice semantics.
	return &Catalog{resources: slices.Clone(resources)}, nil
}

func (c *Catalog) ListResources(ctx context.Context, limit int) ([]catalog.Resource, error) {
	if ctx == nil {
		return nil, errContextRequired
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if limit <= 0 {
		return nil, errLimitInvalid
	}

	resultSize := min(limit, len(c.resources))

	// Preserve the catalog's nil or non-nil result shape.
	return slices.Clone(c.resources[:resultSize]), nil
}
