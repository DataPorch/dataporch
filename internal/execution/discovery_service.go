package execution

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/adamraziv/dataporch/internal/access"
	"github.com/adamraziv/dataporch/internal/connection"
)

var (
	errSourceRegistryRequired = errors.New("execution: source registry is required")
	errRelationalAuthorizer   = errors.New("execution: authorizer is required")
	errDiscovererRequired     = errors.New("execution: relational discoverer is required")
	errDiscovererKind         = errors.New("execution: relational discoverer kind is required")
	errDuplicateDiscoverer    = errors.New("execution: duplicate relational discoverer kind")
)

type Service struct {
	sources    SourceRegistry
	authorizer Authorizer
	maxLimit   int
	relational map[connection.Kind]RelationalDiscoverer
}

func New(dependencies Dependencies) (*Service, error) {
	if isNilInterface(dependencies.Sources) {
		return nil, errSourceRegistryRequired
	}
	if isNilInterface(dependencies.Authorizer) {
		return nil, errRelationalAuthorizer
	}
	if dependencies.MaxLimit <= 0 {
		return nil, fmt.Errorf("%w: maximum must be positive", ErrInvalidLimit)
	}

	relational := make(map[connection.Kind]RelationalDiscoverer, len(dependencies.RelationalDiscoverers))
	for _, discoverer := range dependencies.RelationalDiscoverers {
		if isNilInterface(discoverer) {
			return nil, errDiscovererRequired
		}
		kind := discoverer.Kind()
		if kind == "" {
			return nil, errDiscovererKind
		}
		if _, exists := relational[kind]; exists {
			return nil, fmt.Errorf("%w: %q", errDuplicateDiscoverer, kind)
		}
		relational[kind] = discoverer
	}

	return &Service{
		sources:    dependencies.Sources,
		authorizer: dependencies.Authorizer,
		maxLimit:   dependencies.MaxLimit,
		relational: relational,
	}, nil
}

func (s *Service) ListDataSources(ctx context.Context, request ListDataSourcesRequest) (ListDataSourcesResult, error) {
	if ctx == nil {
		return ListDataSourcesResult{}, errContextRequired
	}

	limit, err := s.effectiveLimit(request.Limit)
	if err != nil {
		return ListDataSourcesResult{}, err
	}
	cursorRequest := cursorRequest{
		Operation: "data_source.list",
		Limit:     limit,
		Search:    request.Search,
	}
	payload, err := decodeCursor(request.Cursor, cursorRequest, false)
	if err != nil {
		return ListDataSourcesResult{}, err
	}

	if err := s.authorizer.Authorize(ctx, access.ActionListDataSources); err != nil {
		return ListDataSourcesResult{}, fmt.Errorf("%w: %w", ErrDataPorchAccessDenied, err)
	}

	definitions := s.sources.List()
	sources := make([]DataSource, 0, len(definitions))
	search := strings.ToLower(request.Search)
	for _, definition := range definitions {
		id := string(definition.ID)
		if search != "" && !strings.Contains(strings.ToLower(id), search) {
			continue
		}

		capabilities := make([]Capability, 0, 1)
		if _, supported := s.relational[definition.Kind]; supported {
			capabilities = append(capabilities, CapabilityRelationalDatabase)
		}
		sources = append(sources, DataSource{
			ID:           definition.ID,
			Kind:         definition.Kind,
			Capabilities: capabilities,
		})
	}

	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	if request.Cursor != "" {
		position := payload.LastName
		start := sort.Search(len(sources), func(index int) bool { return sources[index].ID > connection.ID(position) })
		sources = sources[start:]
	}

	hasMore := len(sources) > limit
	if hasMore {
		sources = sources[:limit]
	}
	result := ListDataSourcesResult{Sources: cloneDataSources(sources)}
	if hasMore && len(sources) > 0 {
		result.NextCursor, err = encodeCursor(cursorRequest, string(sources[len(sources)-1].ID), 0)
		if err != nil {
			return ListDataSourcesResult{}, fmt.Errorf("encoding next cursor: %w", err)
		}
	}

	return result, nil
}

func (s *Service) effectiveLimit(value *int) (int, error) {
	if value == nil {
		return s.maxLimit, nil
	}
	if *value <= 0 || *value > s.maxLimit {
		return 0, fmt.Errorf("%w: must be between 1 and %d", ErrInvalidRequest, s.maxLimit)
	}
	return *value, nil
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() { //nolint:exhaustive // Other kinds cannot be nil.
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func cloneDataSources(sources []DataSource) []DataSource {
	cloned := make([]DataSource, len(sources))
	for index, source := range sources {
		capabilities := make([]Capability, len(source.Capabilities))
		copy(capabilities, source.Capabilities)
		cloned[index] = DataSource{
			ID:           source.ID,
			Kind:         source.Kind,
			Capabilities: capabilities,
		}
	}
	if cloned == nil {
		return []DataSource{}
	}
	return cloned
}
