package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	relationalQueryOperation         = "relational_database.query"
	maxDatabaseErrorResponseBytes    = 64 << 10
	databaseErrorResponseBudgetRatio = 16
)

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
				byteLimit,
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
				byteLimit,
			)
		}

		if encodedSize > byteLimit {
			return nil, execution.RelationalQueryResult{}, finishRelationalQueryError(
				ctx,
				logger,
				request,
				start,
				execution.ErrResultTooLarge,
				byteLimit,
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
	byteLimit int,
) error {
	failure := execution.ClassifyRelationalQuery(ctx, err)
	boundedFailure, bounds := boundRelationalQueryFailure(failure, byteLimit)
	responseFailure := boundedFailure

	message, encodedSize, encodeErr := relationalQueryFailureWire(responseFailure)
	if encodeErr != nil || encodedSize > byteLimit {
		responseFailure = execution.ClassifyRelationalQuery(ctx, execution.ErrResultTooLarge)
		message, _, encodeErr = relationalQueryFailureWire(responseFailure)
	}

	if encodeErr != nil {
		responseFailure = execution.Failure{
			Category:  execution.ErrorCategoryInternal,
			Message:   "The query operation failed safely.",
			Retryable: false,
		}
		message = ""
	}

	fields := queryLogFields(request, start)

	fields = append(
		fields,
		slog.String("category", string(failure.Category)),
		slog.Bool("retryable", failure.Retryable),
	)
	if boundedFailure.DatabaseError != nil {
		fields = append(fields, databaseErrorLogGroup(boundedFailure.DatabaseError, bounds))
	}

	logger.WarnContext(ctx, "relational query failed", fields...)

	return &toolExecutionError{failure: responseFailure, cause: err, message: message}
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

type databaseErrorBounds struct {
	originalBytes int
	truncated     bool
}

func boundRelationalQueryFailure(
	failure execution.Failure,
	byteLimit int,
) (execution.Failure, databaseErrorBounds) {
	if failure.DatabaseError == nil {
		return failure, databaseErrorBounds{}
	}

	budget := min(byteLimit/databaseErrorResponseBudgetRatio, maxDatabaseErrorResponseBytes)

	databaseError, bounds := boundDatabaseError(failure.DatabaseError, budget)

	failure.DatabaseError = databaseError
	if databaseError.Message == "" {
		failure.Message = "The database rejected the query."
	} else {
		failure.Message = databaseError.Message
	}

	return failure, bounds
}

func boundDatabaseError(
	databaseError *execution.DatabaseError,
	budget int,
) (*execution.DatabaseError, databaseErrorBounds) {
	if databaseError == nil {
		return nil, databaseErrorBounds{}
	}

	bounded := *databaseError
	remaining := budget
	bounds := databaseErrorBounds{}

	boundString := func(value string) string {
		bounds.originalBytes += len(value)
		boundedValue, truncated := boundUTF8String(value, &remaining)
		bounds.truncated = bounds.truncated || truncated

		return boundedValue
	}

	bounded.Kind = connection.Kind(boundString(string(databaseError.Kind)))
	bounded.Code = boundString(databaseError.Code)
	bounded.Severity = boundString(databaseError.Severity)
	bounded.SeverityUnlocalized = boundString(databaseError.SeverityUnlocalized)
	bounded.Message = boundString(databaseError.Message)
	bounded.Detail = boundString(databaseError.Detail)
	bounded.Hint = boundString(databaseError.Hint)
	bounded.InternalQuery = boundString(databaseError.InternalQuery)
	bounded.Where = boundString(databaseError.Where)
	bounded.SchemaName = boundString(databaseError.SchemaName)
	bounded.TableName = boundString(databaseError.TableName)
	bounded.ColumnName = boundString(databaseError.ColumnName)
	bounded.DataTypeName = boundString(databaseError.DataTypeName)
	bounded.ConstraintName = boundString(databaseError.ConstraintName)
	bounded.File = boundString(databaseError.File)
	bounded.Routine = boundString(databaseError.Routine)
	bounded.Truncated = databaseError.Truncated || bounds.truncated
	bounds.truncated = bounded.Truncated

	return &bounded, bounds
}

func boundUTF8String(value string, remaining *int) (string, bool) {
	if value == "" {
		return "", false
	}

	if *remaining <= 0 {
		return "", true
	}

	if len(value) <= *remaining && utf8.ValidString(value) {
		*remaining -= len(value)

		return value, false
	}

	var bounded strings.Builder
	bounded.Grow(min(len(value), *remaining))

	consumed := 0

	for consumed < len(value) {
		current := value[consumed:]
		r, size := utf8.DecodeRuneInString(current)
		encodedSize := size

		if r == utf8.RuneError && size == 1 {
			encodedSize = utf8.RuneLen(utf8.RuneError)
		}

		if encodedSize > *remaining {
			break
		}

		bounded.WriteRune(r)

		*remaining -= encodedSize
		consumed += size
	}

	return bounded.String(), consumed < len(value)
}

func relationalQueryFailureWire(failure execution.Failure) (string, int, error) {
	encodedFailure, err := json.Marshal(failure)
	if err != nil {
		return "", 0, fmt.Errorf("marshaling relational query failure: %w", err)
	}

	wireCandidate := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: string(encodedFailure)},
		},
		IsError: true,
	}

	encodedCandidate, err := json.Marshal(wireCandidate)
	if err != nil {
		return "", 0, fmt.Errorf("marshaling relational query failure result: %w", err)
	}

	return string(encodedFailure), len(encodedCandidate), nil
}

func relationalQueryFailureWireSize(failure execution.Failure) (int, error) {
	_, size, err := relationalQueryFailureWire(failure)

	return size, err
}

func databaseErrorLogGroup(
	databaseError *execution.DatabaseError,
	bounds databaseErrorBounds,
) slog.Attr {
	if databaseError == nil {
		return slog.Group("database_error")
	}

	attrs := make([]any, 0, 20)
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

	if bounds.truncated {
		attrs = append(
			attrs,
			slog.Bool("truncated", true),
			slog.Int("original_size_bytes", bounds.originalBytes),
		)
	}

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
