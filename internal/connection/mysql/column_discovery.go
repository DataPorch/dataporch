package mysql

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/adamraziv/dataporch/internal/execution"
)

const resolveRelationSQL = `
SELECT TABLE_TYPE
FROM INFORMATION_SCHEMA.TABLES
WHERE TABLE_SCHEMA = ?
  AND TABLE_NAME = ?
  AND TABLE_TYPE IN ('BASE TABLE', 'VIEW')`

const listColumnsSQL = `
SELECT
    COLUMN_NAME,
    ORDINAL_POSITION,
    COLUMN_TYPE,
    DATA_TYPE,
    CHARACTER_MAXIMUM_LENGTH,
    NUMERIC_PRECISION,
    NUMERIC_SCALE,
    DATETIME_PRECISION,
    IS_NULLABLE,
    COLUMN_DEFAULT,
    EXTRA,
    GENERATION_EXPRESSION,
    CASE WHEN ? THEN NULLIF(COLUMN_COMMENT, '') END
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = ?
  AND TABLE_NAME = ?
  AND (? = '' OR LOCATE(LOWER(?), LOWER(COLUMN_NAME)) > 0)
  AND ORDINAL_POSITION > ?
ORDER BY ORDINAL_POSITION
LIMIT ?`

func (d *Discoverer) ListColumns(
	ctx context.Context,
	request execution.ColumnDiscoveryRequest,
) (page execution.ColumnDiscoveryPage, retErr error) {
	client, err := d.open(ctx, request.SourceID)
	if err != nil {
		return page, err
	}
	if request.Schema != client.database {
		return page, execution.ErrSchemaNotFound
	}

	queryCtx, cancel := d.queryContext(ctx)
	defer cancel()

	relationKindValue, err := resolveRelation(
		ctx,
		queryCtx,
		client.pool,
		request.Schema,
		request.Table,
	)
	if err != nil {
		return page, err
	}

	rows, err := client.pool.Query(
		queryCtx,
		listColumnsSQL,
		request.IncludeDescriptions,
		request.Schema,
		request.Table,
		request.Search,
		request.Search,
		request.AfterOrdinal,
		request.Limit+1,
	)
	if err != nil {
		return page, classifyDiscoveryQueryError(ctx, queryCtx, err)
	}
	if isNilInterface(rows) {
		return page, fmt.Errorf("%w: nil catalog rows", execution.ErrInternal)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			retErr = errors.Join(
				retErr,
				fmt.Errorf("%w: closing catalog rows: %w", execution.ErrInternal, closeErr),
			)
		}
	}()

	page.Columns = make([]execution.Column, 0, request.Limit+1)
	for rows.Next() {
		column, err := scanColumn(rows, request.IncludeDescriptions)
		if err != nil {
			return page, classifyDiscoveryQueryError(ctx, queryCtx, err)
		}
		page.Columns = append(page.Columns, column)
	}

	if err := rows.Err(); err != nil {
		return page, classifyDiscoveryQueryError(ctx, queryCtx, err)
	}

	page.RelationKind = relationKindValue
	page.Constraints = make([]execution.Constraint, 0)
	if len(page.Columns) > request.Limit {
		page.HasMore = true
		page.Columns = page.Columns[:request.Limit]
	}
	return page, nil
}

func resolveRelation(
	parentCtx context.Context,
	queryCtx context.Context,
	pool runtimePool,
	schema string,
	table string,
) (kind execution.RelationKind, retErr error) {
	rows, err := pool.Query(queryCtx, resolveRelationSQL, schema, table)
	if err != nil {
		return "", classifyDiscoveryQueryError(parentCtx, queryCtx, err)
	}
	if isNilInterface(rows) {
		return "", fmt.Errorf("%w: nil relation rows", execution.ErrInternal)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			retErr = errors.Join(
				retErr,
				fmt.Errorf("%w: closing relation rows: %w", execution.ErrInternal, closeErr),
			)
		}
	}()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", classifyDiscoveryQueryError(parentCtx, queryCtx, err)
		}
		return "", execution.ErrRelationNotFound
	}

	var tableType string
	if err := rows.Scan(&tableType); err != nil {
		return "", classifyDiscoveryQueryError(parentCtx, queryCtx, err)
	}
	if err := rows.Err(); err != nil {
		return "", classifyDiscoveryQueryError(parentCtx, queryCtx, err)
	}

	return relationKind(tableType)
}

func scanColumn(rows catalogRows, includeDescriptions bool) (execution.Column, error) {
	var (
		columnName           string
		ordinal              int64
		formattedType        string
		typeName             string
		characterLength      *int64
		numericPrecision     *int64
		numericScale         *int64
		temporalPrecision    *int64
		nullable             string
		defaultExpression    *string
		extra                string
		generationExpression *string
		description          *string
	)

	if err := rows.Scan(
		&columnName,
		&ordinal,
		&formattedType,
		&typeName,
		&characterLength,
		&numericPrecision,
		&numericScale,
		&temporalPrecision,
		&nullable,
		&defaultExpression,
		&extra,
		&generationExpression,
		&description,
	); err != nil {
		return execution.Column{}, err
	}

	if ordinal <= 0 || ordinal > math.MaxInt {
		return execution.Column{}, fmt.Errorf("%w: invalid column ordinal", execution.ErrInternal)
	}

	generated := columnGenerated(extra, generationExpression)
	if generated != nil {
		defaultExpression = nil
	}
	if !includeDescriptions {
		description = nil
	}

	return execution.Column{
		Name:            columnName,
		OrdinalPosition: int(ordinal),
		FormattedType:   formattedType,
		Type: execution.DataType{
			Name:              typeName,
			Category:          typeCategory(typeName),
			Length:            int32Pointer(characterLength),
			Precision:         int32Pointer(numericPrecision),
			Scale:             int32Pointer(numericScale),
			TemporalPrecision: int32Pointer(temporalPrecision),
		},
		Nullable:          strings.EqualFold(nullable, "YES"),
		DefaultExpression: defaultExpression,
		Identity:          columnIdentity(extra),
		Generated:         generated,
		Description:       description,
	}, nil
}

func typeCategory(typeName string) execution.TypeCategory {
	switch strings.ToLower(typeName) {
	case "enum":
		return execution.TypeCategoryEnum
	case "set":
		return execution.TypeCategoryOther
	default:
		return execution.TypeCategoryBase
	}
}

func int32Pointer(value *int64) *int32 {
	if value == nil || *value < 0 || *value > math.MaxInt32 {
		return nil
	}

	converted := int32(*value)
	return &converted
}

func columnIdentity(extra string) *execution.Identity {
	if strings.Contains(strings.ToUpper(extra), "AUTO_INCREMENT") {
		return &execution.Identity{Generation: "by_default"}
	}
	return nil
}

func columnGenerated(extra string, expression *string) *execution.Generated {
	normalized := strings.ToUpper(extra)
	switch {
	case strings.Contains(normalized, "VIRTUAL GENERATED"):
		return &execution.Generated{Kind: "virtual", Expression: stringValue(expression)}
	case strings.Contains(normalized, "STORED GENERATED"):
		return &execution.Generated{Kind: "stored", Expression: stringValue(expression)}
	default:
		return nil
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
