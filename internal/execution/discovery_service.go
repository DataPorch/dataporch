package execution

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/adamraziv/dataporch/internal/access"
	"github.com/adamraziv/dataporch/internal/connection"
)

var (
	errSourceRegistryRequired           = errors.New("execution: source registry is required")
	errRelationalAuthorizer             = errors.New("execution: authorizer is required")
	errDiscovererRequired               = errors.New("execution: relational discoverer is required")
	errDiscovererKind                   = errors.New("execution: relational discoverer kind is required")
	errDuplicateDiscoverer              = errors.New("execution: duplicate relational discoverer kind")
	errRelationalQueryExecutorRequired  = errors.New("execution: relational query executor is required")
	errRelationalQueryExecutorKind      = errors.New("execution: relational query executor kind is required")
	errDuplicateRelationalQueryExecutor = errors.New("execution: duplicate relational query executor kind")
)

type Service struct {
	sources           SourceRegistry
	authorizer        Authorizer
	maxLimit          int
	relational        map[connection.Kind]RelationalDiscoverer
	relationalQueries map[connection.Kind]RelationalQueryExecutor
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

	relationalQueries := make(
		map[connection.Kind]RelationalQueryExecutor,
		len(dependencies.RelationalQueryExecutors),
	)
	for _, executor := range dependencies.RelationalQueryExecutors {
		if isNilInterface(executor) {
			return nil, errRelationalQueryExecutorRequired
		}

		kind := executor.Kind()
		if kind == "" {
			return nil, errRelationalQueryExecutorKind
		}

		if _, exists := relationalQueries[kind]; exists {
			return nil, fmt.Errorf("%w: %q", errDuplicateRelationalQueryExecutor, kind)
		}

		relationalQueries[kind] = executor
	}

	return &Service{
		sources:           dependencies.Sources,
		authorizer:        dependencies.Authorizer,
		maxLimit:          dependencies.MaxLimit,
		relational:        relational,
		relationalQueries: relationalQueries,
	}, nil
}

//nolint:gocyclo // This boundary validates, authorizes, snapshots, and pages a source-list request.
func (s *Service) ListDataSources(ctx context.Context, request ListDataSourcesRequest) (ListDataSourcesResult, error) {
	if err := validateContext(ctx); err != nil {
		return ListDataSourcesResult{}, err
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

	if err := s.authorizer.Authorize(ctx, access.Request{
		Action: access.ActionListDataSources,
	}); err != nil {
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

func (s *Service) ListRelationalSchemas(
	ctx context.Context,
	request ListRelationalSchemasRequest,
) (ListRelationalSchemasResult, error) {
	if err := validateContext(ctx); err != nil {
		return ListRelationalSchemasResult{}, err
	}

	limit, err := s.effectiveLimit(request.Limit)
	if err != nil {
		return ListRelationalSchemasResult{}, err
	}

	if err := validateSourceID(request.SourceID); err != nil {
		return ListRelationalSchemasResult{}, err
	}

	cursorRequest := cursorRequest{
		Operation:           "relational_database.list_schemas",
		SourceID:            string(request.SourceID),
		Limit:               limit,
		Search:              request.Search,
		IncludeDescriptions: request.IncludeDescriptions,
	}

	payload, err := decodeCursor(request.Cursor, cursorRequest, false)
	if err != nil {
		return ListRelationalSchemasResult{}, err
	}

	discoverer, err := s.relationalDiscoverer(ctx, request.SourceID, access.ActionListRelationalSchemas)
	if err != nil {
		return ListRelationalSchemasResult{}, err
	}

	page, err := discoverer.ListSchemas(ctx, SchemaDiscoveryRequest{
		SourceID:            request.SourceID,
		Search:              request.Search,
		IncludeDescriptions: request.IncludeDescriptions,
		Limit:               limit,
		AfterName:           payload.LastName,
	})
	if err != nil {
		return ListRelationalSchemasResult{}, err
	}

	if len(page.Schemas) > limit {
		return ListRelationalSchemasResult{}, fmt.Errorf("%w: schema discoverer exceeded result limit", ErrInternal)
	}

	result := ListRelationalSchemasResult{
		SourceID: request.SourceID,
		Schemas:  cloneSchemas(page.Schemas),
	}
	if page.HasMore && len(page.Schemas) > 0 {
		result.NextCursor, err = encodeCursor(cursorRequest, page.Schemas[len(page.Schemas)-1].Name, 0)
		if err != nil {
			return ListRelationalSchemasResult{}, fmt.Errorf("encoding next cursor: %w", err)
		}
	}

	return result, nil
}

func (s *Service) ListRelationalTables(
	ctx context.Context,
	request ListRelationalTablesRequest,
) (ListRelationalTablesResult, error) {
	if err := validateContext(ctx); err != nil {
		return ListRelationalTablesResult{}, err
	}

	limit, err := s.effectiveLimit(request.Limit)
	if err != nil {
		return ListRelationalTablesResult{}, err
	}

	if err := validateSourceID(request.SourceID); err != nil {
		return ListRelationalTablesResult{}, err
	}

	if err := validateIdentifier(request.Schema); err != nil {
		return ListRelationalTablesResult{}, fmt.Errorf("%w: schema", err)
	}

	cursorRequest := cursorRequest{
		Operation:           "relational_database.list_tables",
		SourceID:            string(request.SourceID),
		Schema:              request.Schema,
		Limit:               limit,
		Search:              request.Search,
		IncludeDescriptions: request.IncludeDescriptions,
	}

	payload, err := decodeCursor(request.Cursor, cursorRequest, false)
	if err != nil {
		return ListRelationalTablesResult{}, err
	}

	discoverer, err := s.relationalDiscoverer(ctx, request.SourceID, access.ActionListRelationalTables)
	if err != nil {
		return ListRelationalTablesResult{}, err
	}

	page, err := discoverer.ListTables(ctx, TableDiscoveryRequest{
		SourceID:            request.SourceID,
		Schema:              request.Schema,
		Search:              request.Search,
		IncludeDescriptions: request.IncludeDescriptions,
		Limit:               limit,
		AfterName:           payload.LastName,
	})
	if err != nil {
		return ListRelationalTablesResult{}, err
	}

	if len(page.Tables) > limit {
		return ListRelationalTablesResult{}, fmt.Errorf("%w: table discoverer exceeded result limit", ErrInternal)
	}

	result := ListRelationalTablesResult{
		SourceID: request.SourceID,
		Schema:   request.Schema,
		Tables:   cloneTables(page.Tables),
	}
	if page.HasMore && len(page.Tables) > 0 {
		result.NextCursor, err = encodeCursor(cursorRequest, page.Tables[len(page.Tables)-1].Name, 0)
		if err != nil {
			return ListRelationalTablesResult{}, fmt.Errorf("encoding next cursor: %w", err)
		}
	}

	return result, nil
}

//nolint:gocyclo // This boundary validates, authorizes, dispatches, and pages a column-list request.
func (s *Service) ListRelationalColumns(
	ctx context.Context,
	request ListRelationalColumnsRequest,
) (ListRelationalColumnsResult, error) {
	if err := validateContext(ctx); err != nil {
		return ListRelationalColumnsResult{}, err
	}

	limit, err := s.effectiveLimit(request.Limit)
	if err != nil {
		return ListRelationalColumnsResult{}, err
	}

	if err := validateSourceID(request.SourceID); err != nil {
		return ListRelationalColumnsResult{}, err
	}

	if err := validateIdentifier(request.Schema); err != nil {
		return ListRelationalColumnsResult{}, fmt.Errorf("%w: schema", err)
	}

	if err := validateIdentifier(request.Table); err != nil {
		return ListRelationalColumnsResult{}, fmt.Errorf("%w: table", err)
	}

	cursorRequest := cursorRequest{
		Operation:           "relational_database.list_columns",
		SourceID:            string(request.SourceID),
		Schema:              request.Schema,
		Table:               request.Table,
		Limit:               limit,
		Search:              request.Search,
		IncludeDescriptions: request.IncludeDescriptions,
	}

	payload, err := decodeCursor(request.Cursor, cursorRequest, true)
	if err != nil {
		return ListRelationalColumnsResult{}, err
	}

	discoverer, err := s.relationalDiscoverer(ctx, request.SourceID, access.ActionListRelationalColumns)
	if err != nil {
		return ListRelationalColumnsResult{}, err
	}

	page, err := discoverer.ListColumns(ctx, ColumnDiscoveryRequest{
		SourceID:            request.SourceID,
		Schema:              request.Schema,
		Table:               request.Table,
		Search:              request.Search,
		IncludeDescriptions: request.IncludeDescriptions,
		Limit:               limit,
		AfterOrdinal:        payload.LastOrdinal,
	})
	if err != nil {
		return ListRelationalColumnsResult{}, err
	}

	if len(page.Columns) > limit {
		return ListRelationalColumnsResult{}, fmt.Errorf("%w: column discoverer exceeded result limit", ErrInternal)
	}

	if err := validateRelationKind(page.RelationKind); err != nil {
		return ListRelationalColumnsResult{}, err
	}

	result := ListRelationalColumnsResult{
		SourceID:     request.SourceID,
		Schema:       request.Schema,
		Table:        request.Table,
		RelationKind: page.RelationKind,
		Columns:      cloneColumns(page.Columns),
		Constraints:  cloneConstraints(page.Constraints),
	}
	if page.HasMore && len(page.Columns) > 0 {
		result.NextCursor, err = encodeCursor(cursorRequest, "", page.Columns[len(page.Columns)-1].OrdinalPosition)
		if err != nil {
			return ListRelationalColumnsResult{}, fmt.Errorf("encoding next cursor: %w", err)
		}
	}

	return result, nil
}

func (s *Service) relationalDiscoverer(
	ctx context.Context,
	sourceID connection.ID,
	action access.Action,
) (RelationalDiscoverer, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}

	if err := validateSourceID(sourceID); err != nil {
		return nil, err
	}

	if err := s.authorizer.Authorize(ctx, access.Request{
		Action:   action,
		SourceID: sourceID,
	}); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDataPorchAccessDenied, err)
	}

	definition, err := s.sources.Lookup(sourceID)
	if err != nil {
		if errors.Is(err, connection.ErrDatabaseNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrSourceNotFound, sourceID)
		}

		return nil, fmt.Errorf("%w: source lookup failed", ErrInternal)
	}

	discoverer, ok := s.relational[definition.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSourceCapability, sourceID)
	}

	return discoverer, nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return errContextRequired
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrCancelled, err)
	}

	return nil
}

func validateSourceID(sourceID connection.ID) error {
	if err := sourceID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	return nil
}

func validateIdentifier(value string) error {
	if value == "" || !utf8.ValidString(value) {
		return ErrInvalidRequest
	}

	return nil
}

func validateRelationKind(kind RelationKind) error {
	switch kind {
	case RelationKindTable,
		RelationKindPartitionedTable,
		RelationKindView,
		RelationKindMaterializedView,
		RelationKindForeignTable,
		RelationKindVirtualTable:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedRelationKind, kind)
	}
}

func cloneSchemas(values []Schema) []Schema {
	cloned := make([]Schema, len(values))
	for index, value := range values {
		cloned[index] = Schema{Name: value.Name, Description: cloneStringPointer(value.Description)}
	}

	return cloned
}

func cloneTables(values []Table) []Table {
	cloned := make([]Table, len(values))
	for index, value := range values {
		cloned[index] = Table{Name: value.Name, Kind: value.Kind, Description: cloneStringPointer(value.Description)}
	}

	return cloned
}

func cloneColumns(values []Column) []Column {
	cloned := make([]Column, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].Type = cloneDataType(value.Type)

		cloned[index].DefaultExpression = cloneStringPointer(value.DefaultExpression)
		if value.Identity != nil {
			identity := *value.Identity
			cloned[index].Identity = &identity
		}

		if value.Generated != nil {
			generated := *value.Generated
			cloned[index].Generated = &generated
		}

		cloned[index].Description = cloneStringPointer(value.Description)
	}

	return cloned
}

func cloneDataType(value DataType) DataType {
	cloned := value
	cloned.Length = cloneInt32Pointer(value.Length)
	cloned.Precision = cloneInt32Pointer(value.Precision)
	cloned.Scale = cloneInt32Pointer(value.Scale)

	cloned.TemporalPrecision = cloneInt32Pointer(value.TemporalPrecision)
	if value.ElementType != nil {
		element := *value.ElementType
		cloned.ElementType = &element
	}

	if value.DomainBaseType != nil {
		base := *value.DomainBaseType
		cloned.DomainBaseType = &base
	}

	return cloned
}

func cloneConstraints(values []Constraint) []Constraint {
	cloned := make([]Constraint, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].Columns = make([]string, len(value.Columns))
		copy(cloned[index].Columns, value.Columns)

		if value.Referenced != nil {
			referenced := *value.Referenced
			referenced.Columns = make([]string, len(value.Referenced.Columns))
			copy(referenced.Columns, value.Referenced.Columns)
			cloned[index].Referenced = &referenced
		}

		cloned[index].NullsNotDistinct = cloneBoolPointer(value.NullsNotDistinct)
		cloned[index].CheckExpression = cloneStringPointer(value.CheckExpression)
	}

	return cloned
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}

	cloned := *value

	return &cloned
}

func cloneInt32Pointer(value *int32) *int32 {
	if value == nil {
		return nil
	}

	cloned := *value

	return &cloned
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}

	cloned := *value

	return &cloned
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

	return cloned
}
