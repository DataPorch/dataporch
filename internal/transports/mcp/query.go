package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const relationalQueryOperation = "relational_database.query"

type relationalQueryInput struct {
	Kind     connection.Kind `json:"kind" jsonschema:"database kind; currently postgres"`
	SourceID connection.ID   `json:"source_id" jsonschema:"globally unique configured source identifier"`
	Query    string          `json:"query" jsonschema:"one complete PostgreSQL statement"`
}

func relationalQueryAnnotations() *mcpsdk.ToolAnnotations {
	return &mcpsdk.ToolAnnotations{
		ReadOnlyHint:   true,
		IdempotentHint: false,
		OpenWorldHint:  boolPointer(false),
	}
}

func relationalQueryCallToolResult(
	output execution.RelationalQueryResult,
) (*mcpsdk.CallToolResult, int, error) {
	outputJSON, err := json.Marshal(output)
	if err != nil {
		return nil, 0, fmt.Errorf("marshaling relational query output: %w", err)
	}

	var normalizedOutput map[string]any
	if err := json.Unmarshal(outputJSON, &normalizedOutput); err != nil {
		return nil, 0, fmt.Errorf("normalizing relational query output: %w", err)
	}

	normalizedJSON, err := json.Marshal(normalizedOutput)
	if err != nil {
		return nil, 0, fmt.Errorf("marshaling normalized relational query output: %w", err)
	}

	content := []mcpsdk.Content{
		&mcpsdk.TextContent{Text: string(normalizedJSON)},
	}

	wireCandidate := &mcpsdk.CallToolResult{
		Content:           content,
		StructuredContent: json.RawMessage(normalizedJSON),
	}

	encodedCandidate, err := json.Marshal(wireCandidate)
	if err != nil {
		return nil, 0, fmt.Errorf("marshaling relational query tool result: %w", err)
	}

	return &mcpsdk.CallToolResult{Content: content}, len(encodedCandidate), nil
}

func relationalQueryToolHandler(
	querier RelationalQuerier,
	logger *slog.Logger,
	byteLimit int,
) mcpsdk.ToolHandlerFor[relationalQueryInput, execution.RelationalQueryResult] {
	return func(
		ctx context.Context,
		_ *mcpsdk.CallToolRequest,
		input relationalQueryInput,
	) (*mcpsdk.CallToolResult, execution.RelationalQueryResult, error) {
		start := time.Now()
		request := execution.RelationalQueryRequest{
			Kind:     input.Kind,
			SourceID: input.SourceID,
			Query:    input.Query,
		}

		output, err := querier.QueryRelationalDatabase(ctx, request)
		if err != nil {
			return nil, execution.RelationalQueryResult{}, finishRelationalQueryError(
				ctx,
				logger,
				request,
				start,
				err,
			)
		}

		result, encodedSize, err := relationalQueryCallToolResult(output)
		if err != nil {
			return nil, execution.RelationalQueryResult{}, finishRelationalQueryError(
				ctx,
				logger,
				request,
				start,
				fmt.Errorf("%w: encoding query result", execution.ErrInternal),
			)
		}

		if encodedSize > byteLimit {
			return nil, execution.RelationalQueryResult{}, finishRelationalQueryError(
				ctx,
				logger,
				request,
				start,
				execution.ErrResultTooLarge,
			)
		}

		finishRelationalQuerySuccess(ctx, logger, request, output, start)

		return result, output, nil
	}
}

func finishRelationalQuerySuccess(
	ctx context.Context,
	logger *slog.Logger,
	request execution.RelationalQueryRequest,
	output execution.RelationalQueryResult,
	start time.Time,
) {
	fields := queryLogFields(request, start)
	fields = append(
		fields,
		slog.Int("row_count", output.RowCount),
		slog.Bool("truncated", output.Truncated),
	)

	logger.InfoContext(ctx, "relational query completed", fields...)
}

func finishRelationalQueryError(
	ctx context.Context,
	logger *slog.Logger,
	request execution.RelationalQueryRequest,
	start time.Time,
	err error,
) error {
	failure := execution.ClassifyRelationalQuery(ctx, err)
	fields := queryLogFields(request, start)

	fields = append(
		fields,
		slog.String("category", string(failure.Category)),
		slog.Bool("retryable", failure.Retryable),
	)
	if failure.DatabaseError != nil {
		fields = append(fields, databaseErrorLogGroup(failure.DatabaseError))
	}

	logger.WarnContext(ctx, "relational query failed", fields...)

	return &toolExecutionError{failure: failure, cause: err}
}

func queryLogFields(request execution.RelationalQueryRequest, start time.Time) []any {
	return []any{
		slog.String("operation", relationalQueryOperation),
		slog.String("query", request.Query),
		slog.String("kind", string(request.Kind)),
		slog.String("source_id", string(request.SourceID)),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
	}
}

func databaseErrorLogGroup(databaseError *execution.DatabaseError) slog.Attr {
	if databaseError == nil {
		return slog.Group("database_error")
	}

	attrs := make([]any, 0, 18)
	appendString := func(name, value string) {
		if value != "" {
			attrs = append(attrs, slog.String(name, value))
		}
	}
	appendInt := func(name string, value int32) {
		if value != 0 {
			attrs = append(attrs, slog.Int64(name, int64(value)))
		}
	}

	appendString("kind", string(databaseError.Kind))
	appendString("code", databaseError.Code)
	appendString("severity", databaseError.Severity)
	appendString("severity_unlocalized", databaseError.SeverityUnlocalized)
	appendString("message", databaseError.Message)
	appendString("detail", databaseError.Detail)
	appendString("hint", databaseError.Hint)
	appendInt("position", databaseError.Position)
	appendInt("internal_position", databaseError.InternalPosition)
	appendString("internal_query", databaseError.InternalQuery)
	appendString("where", databaseError.Where)
	appendString("schema_name", databaseError.SchemaName)
	appendString("table_name", databaseError.TableName)
	appendString("column_name", databaseError.ColumnName)
	appendString("data_type_name", databaseError.DataTypeName)
	appendString("constraint_name", databaseError.ConstraintName)
	appendString("file", databaseError.File)
	appendInt("line", databaseError.Line)
	appendString("routine", databaseError.Routine)

	return slog.Group("database_error", attrs...)
}
