package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"time"

	"github.com/DataPorch/dataporch/internal/connection"
	"github.com/DataPorch/dataporch/internal/execution"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxRequestBodyBytes      = 1 << 20
	serverVersion            = "dev"
	listDataSourcesOperation = "data_source.list"
)

var (
	errDiscovererRequired        = errors.New("mcp: discoverer is required")
	errRelationalQuerierRequired = errors.New("mcp: relational querier is required")
	errQueryByteLimitRequired    = errors.New("mcp: query response byte limit cannot encode a bounded failure")
	errLoggerRequired            = errors.New("mcp: logger is required")
)

type Discoverer interface {
	ListDataSources(context.Context, execution.ListDataSourcesRequest) (execution.ListDataSourcesResult, error)
	ListRelationalSchemas(
		context.Context,
		execution.ListRelationalSchemasRequest,
	) (execution.ListRelationalSchemasResult, error)
	ListRelationalTables(
		context.Context,
		execution.ListRelationalTablesRequest,
	) (execution.ListRelationalTablesResult, error)
	ListRelationalColumns(
		context.Context,
		execution.ListRelationalColumnsRequest,
	) (execution.ListRelationalColumnsResult, error)
}

type RelationalQuerier interface {
	QueryRelationalDatabase(
		context.Context,
		execution.RelationalQueryRequest,
	) (execution.RelationalQueryResult, error)
}

type Dependencies struct {
	Discoverer             Discoverer
	RelationalQuerier      RelationalQuerier
	QueryResponseByteLimit int
	Logger                 *slog.Logger
}

type listDataSourcesInput struct {
	Search string `json:"search,omitempty" jsonschema:"case-insensitive literal source ID substring"`
	Limit  *int   `json:"limit,omitempty" jsonschema:"maximum sources to return"`
	Cursor string `json:"cursor,omitempty" jsonschema:"opaque continuation cursor"`
}

type listSchemasInput struct {
	SourceID            connection.ID `json:"source_id" jsonschema:"configured source identifier"`
	Search              string        `json:"search,omitempty" jsonschema:"case-insensitive literal schema name substring"`
	IncludeDescriptions bool          `json:"include_descriptions,omitempty" jsonschema:"include PostgreSQL comments"`
	Limit               *int          `json:"limit,omitempty" jsonschema:"maximum schemas to return"`
	Cursor              string        `json:"cursor,omitempty" jsonschema:"opaque continuation cursor"`
}

type listTablesInput struct {
	SourceID            connection.ID `json:"source_id" jsonschema:"configured source identifier"`
	Schema              string        `json:"schema" jsonschema:"exact schema name returned by list_schemas"`
	Search              string        `json:"search,omitempty" jsonschema:"case-insensitive literal name substring"`
	IncludeDescriptions bool          `json:"include_descriptions,omitempty" jsonschema:"include PostgreSQL comments"`
	Limit               *int          `json:"limit,omitempty" jsonschema:"maximum relations to return"`
	Cursor              string        `json:"cursor,omitempty" jsonschema:"opaque continuation cursor"`
}

type listColumnsInput struct {
	SourceID            connection.ID `json:"source_id" jsonschema:"configured source identifier"`
	Schema              string        `json:"schema" jsonschema:"exact schema name returned by list_schemas"`
	Table               string        `json:"table" jsonschema:"exact relation name returned by list_tables"`
	Search              string        `json:"search,omitempty" jsonschema:"case-insensitive literal column name substring"`
	IncludeDescriptions bool          `json:"include_descriptions,omitempty" jsonschema:"include PostgreSQL comments"`
	Limit               *int          `json:"limit,omitempty" jsonschema:"maximum columns to return"`
	Cursor              string        `json:"cursor,omitempty" jsonschema:"opaque continuation cursor"`
}

type toolExecutionError struct {
	failure execution.Failure
	cause   error
	message string
}

func (e *toolExecutionError) Error() string {
	if e.message != "" {
		return e.message
	}

	encoded, err := json.Marshal(e.failure)
	if err != nil {
		return `{"category":"internal","message":"The operation failed safely.","retryable":false}`
	}

	return string(encoded)
}

func (e *toolExecutionError) Unwrap() error {
	return e.cause
}

func New(dependencies Dependencies) (http.Handler, error) {
	if isNilInterface(dependencies.Discoverer) {
		return nil, errDiscovererRequired
	}

	if isNilInterface(dependencies.RelationalQuerier) {
		return nil, errRelationalQuerierRequired
	}

	minimumFailureSize, err := relationalQueryFailureWireSize(
		execution.ClassifyRelationalQuery(context.Background(), execution.ErrResultTooLarge),
	)
	if err != nil {
		return nil, fmt.Errorf("calculating minimum query failure size: %w", err)
	}

	if dependencies.QueryResponseByteLimit < minimumFailureSize {
		return nil, errQueryByteLimitRequired
	}

	if dependencies.Logger == nil {
		return nil, errLoggerRequired
	}

	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{
			Name:        "dataporch",
			Title:       "DataPorch",
			Description: "Model-agnostic enterprise data access infrastructure",
			Version:     serverVersion,
		},
		nil,
	)

	annotations := discoveryAnnotations()
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:  listDataSourcesOperation,
		Title: "List data sources",
		Description: "List configured enterprise data sources and their available " +
			"capability families without connecting to them.",
		Annotations: annotations,
	}, dataSourcesToolHandler(dependencies.Discoverer, dependencies.Logger))

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "relational_database.list_schemas",
		Title:       "List relational database schemas",
		Description: "List PostgreSQL schemas accessible through a configured data source.",
		Annotations: annotations,
	}, schemasToolHandler(dependencies.Discoverer, dependencies.Logger))

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:  "relational_database.list_tables",
		Title: "List relational database tables",
		Description: "List readable PostgreSQL tables, views, materialized views, " +
			"partitioned tables, and foreign tables in an exact schema.",
		Annotations: annotations,
	}, tablesToolHandler(dependencies.Discoverer, dependencies.Logger))

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:  "relational_database.list_columns",
		Title: "List relational database columns",
		Description: "List readable PostgreSQL columns, structured types, defaults, " +
			"identity and generated metadata, and relevant constraints.",
		Annotations: annotations,
	}, columnsToolHandler(dependencies.Discoverer, dependencies.Logger))

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        relationalQueryOperation,
		Title:       "Query a relational database",
		Description: "Execute one caller-supplied row-producing statement in a bounded PostgreSQL read-only transaction.",
		Annotations: relationalQueryAnnotations(),
	}, relationalQueryToolHandler(
		dependencies.RelationalQuerier,
		dependencies.Logger,
		dependencies.QueryResponseByteLimit,
	))

	streamableHandler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return server },
		&mcpsdk.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			Logger:                       dependencies.Logger,
			MaxRequestBodyBytes:          maxRequestBodyBytes,
			PropagateRequestCancellation: true,
		},
	)

	originProtection := http.NewCrossOriginProtection()
	protectedHandler := originProtection.Handler(streamableHandler)

	return withOriginValidation(originProtection, protectedHandler), nil
}

func dataSourcesToolHandler(
	discoverer Discoverer,
	logger *slog.Logger,
) func(
	context.Context,
	*mcpsdk.CallToolRequest,
	listDataSourcesInput,
) (*mcpsdk.CallToolResult, execution.ListDataSourcesResult, error) {
	return func(
		ctx context.Context,
		_ *mcpsdk.CallToolRequest,
		input listDataSourcesInput,
	) (*mcpsdk.CallToolResult, execution.ListDataSourcesResult, error) {
		start := time.Now()

		output, err := discoverer.ListDataSources(
			ctx,
			execution.ListDataSourcesRequest{
				Search: input.Search,
				Limit:  input.Limit,
				Cursor: input.Cursor,
			},
		)
		if err != nil {
			return nil, execution.ListDataSourcesResult{}, finishError(
				logger,
				listDataSourcesOperation,
				"",
				start,
				err,
			)
		}

		finishSuccess(logger, listDataSourcesOperation, "", start, len(output.Sources))

		return nil, output, nil
	}
}

func schemasToolHandler(
	discoverer Discoverer,
	logger *slog.Logger,
) func(
	context.Context,
	*mcpsdk.CallToolRequest,
	listSchemasInput,
) (*mcpsdk.CallToolResult, execution.ListRelationalSchemasResult, error) {
	return func(
		ctx context.Context,
		_ *mcpsdk.CallToolRequest,
		input listSchemasInput,
	) (*mcpsdk.CallToolResult, execution.ListRelationalSchemasResult, error) {
		start := time.Now()

		output, err := discoverer.ListRelationalSchemas(
			ctx,
			execution.ListRelationalSchemasRequest{
				SourceID:            input.SourceID,
				Search:              input.Search,
				IncludeDescriptions: input.IncludeDescriptions,
				Limit:               input.Limit,
				Cursor:              input.Cursor,
			},
		)
		if err != nil {
			return nil, execution.ListRelationalSchemasResult{}, finishError(
				logger,
				"relational_database.list_schemas",
				string(input.SourceID),
				start,
				err,
			)
		}

		finishSuccess(
			logger,
			"relational_database.list_schemas",
			string(input.SourceID),
			start,
			len(output.Schemas),
		)

		return nil, output, nil
	}
}

func tablesToolHandler(
	discoverer Discoverer,
	logger *slog.Logger,
) func(
	context.Context,
	*mcpsdk.CallToolRequest,
	listTablesInput,
) (*mcpsdk.CallToolResult, execution.ListRelationalTablesResult, error) {
	return func(
		ctx context.Context,
		_ *mcpsdk.CallToolRequest,
		input listTablesInput,
	) (*mcpsdk.CallToolResult, execution.ListRelationalTablesResult, error) {
		start := time.Now()

		output, err := discoverer.ListRelationalTables(
			ctx,
			execution.ListRelationalTablesRequest{
				SourceID:            input.SourceID,
				Schema:              input.Schema,
				Search:              input.Search,
				IncludeDescriptions: input.IncludeDescriptions,
				Limit:               input.Limit,
				Cursor:              input.Cursor,
			},
		)
		if err != nil {
			return nil, execution.ListRelationalTablesResult{}, finishError(
				logger,
				"relational_database.list_tables",
				string(input.SourceID),
				start,
				err,
			)
		}

		finishSuccess(
			logger,
			"relational_database.list_tables",
			string(input.SourceID),
			start,
			len(output.Tables),
		)

		return nil, output, nil
	}
}

func columnsToolHandler(
	discoverer Discoverer,
	logger *slog.Logger,
) func(
	context.Context,
	*mcpsdk.CallToolRequest,
	listColumnsInput,
) (*mcpsdk.CallToolResult, execution.ListRelationalColumnsResult, error) {
	return func(
		ctx context.Context,
		_ *mcpsdk.CallToolRequest,
		input listColumnsInput,
	) (*mcpsdk.CallToolResult, execution.ListRelationalColumnsResult, error) {
		start := time.Now()

		output, err := discoverer.ListRelationalColumns(
			ctx,
			execution.ListRelationalColumnsRequest{
				SourceID:            input.SourceID,
				Schema:              input.Schema,
				Table:               input.Table,
				Search:              input.Search,
				IncludeDescriptions: input.IncludeDescriptions,
				Limit:               input.Limit,
				Cursor:              input.Cursor,
			},
		)
		if err != nil {
			return nil, execution.ListRelationalColumnsResult{}, finishError(
				logger,
				"relational_database.list_columns",
				string(input.SourceID),
				start,
				err,
			)
		}

		finishSuccess(
			logger,
			"relational_database.list_columns",
			string(input.SourceID),
			start,
			len(output.Columns),
		)

		return nil, output, nil
	}
}

func discoveryAnnotations() *mcpsdk.ToolAnnotations {
	return &mcpsdk.ToolAnnotations{
		DestructiveHint: boolPointer(false),
		IdempotentHint:  true,
		OpenWorldHint:   boolPointer(false),
		ReadOnlyHint:    true,
	}
}

func finishSuccess(logger *slog.Logger, operation, sourceID string, start time.Time, resultCount int) {
	fields := []any{
		slog.String("operation", operation),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		slog.Int("result_count", resultCount),
	}
	if sourceID != "" {
		fields = append(fields, slog.String("source_id", sourceID))
	}

	logger.Debug("discovery operation completed", fields...)
}

func finishError(logger *slog.Logger, operation, sourceID string, start time.Time, err error) error {
	failure := execution.Classify(err)

	fields := []any{
		slog.String("operation", operation),
		slog.String("category", string(failure.Category)),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
	}
	if sourceID != "" {
		fields = append(fields, slog.String("source_id", sourceID))
	}

	logger.Warn("discovery operation failed", fields...)

	return &toolExecutionError{failure: failure, cause: err}
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	kind := reflected.Kind()

	if kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface ||
		kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice {
		return reflected.IsNil()
	}

	return false
}

func boolPointer(value bool) *bool {
	return &value
}
