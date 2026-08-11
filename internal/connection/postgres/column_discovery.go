package postgres

import (
	"context"
	"fmt"
	"math"

	"github.com/adamraziv/dataporch/internal/execution"
)

const (
	typeCategoryBaseCode = "base"

	resolveRelationSQL = `
SELECT
    c.oid,
    c.relkind::text,
    (
      pg_catalog.has_table_privilege(c.oid, 'SELECT')
      OR EXISTS (
        SELECT 1
        FROM pg_catalog.pg_attribute AS a
        WHERE a.attrelid = c.oid
          AND a.attnum > 0
          AND NOT a.attisdropped
          AND pg_catalog.has_column_privilege(c.oid, a.attnum, 'SELECT')
      )
    )
FROM pg_catalog.pg_class AS c
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`

	listColumnsSQL = `
SELECT
    a.attname,
    a.attnum::integer,
    pg_catalog.format_type(a.atttypid, a.atttypmod),
    type_ns.nspname,
    t.typname,
    CASE
      WHEN t.typtype = 'd' THEN 'domain'
      WHEN t.typcategory = 'A' THEN 'array'
      WHEN t.typtype = 'b' THEN 'base'
      WHEN t.typtype = 'e' THEN 'enum'
      WHEN t.typtype = 'c' THEN 'composite'
      WHEN t.typtype = 'r' THEN 'range'
      WHEN t.typtype = 'm' THEN 'multirange'
      WHEN t.typtype = 'p' THEN 'pseudo'
      ELSE 'other'
    END,
    information_schema._pg_char_max_length(
      information_schema._pg_truetypid(a, t),
      information_schema._pg_truetypmod(a, t)
    ),
    information_schema._pg_numeric_precision(
      information_schema._pg_truetypid(a, t),
      information_schema._pg_truetypmod(a, t)
    ),
    information_schema._pg_numeric_scale(
      information_schema._pg_truetypid(a, t),
      information_schema._pg_truetypmod(a, t)
    ),
    information_schema._pg_datetime_precision(
      information_schema._pg_truetypid(a, t),
      information_schema._pg_truetypmod(a, t)
    ),
    t.typcategory = 'A',
    element_ns.nspname,
    element_type.typname,
    base_ns.nspname,
    base_type.typname,
    NOT (
      a.attnotnull
      OR (t.typtype = 'd' AND t.typnotnull)
    ),
    pg_catalog.pg_get_expr(default_value.adbin, default_value.adrelid, true),
    a.attidentity::text,
    a.attgenerated::text,
    CASE WHEN $4 THEN pg_catalog.col_description(a.attrelid, a.attnum) END
FROM pg_catalog.pg_attribute AS a
JOIN pg_catalog.pg_type AS t ON t.oid = a.atttypid
JOIN pg_catalog.pg_namespace AS type_ns ON type_ns.oid = t.typnamespace
LEFT JOIN pg_catalog.pg_type AS element_type ON element_type.oid = t.typelem
LEFT JOIN pg_catalog.pg_namespace AS element_ns ON element_ns.oid = element_type.typnamespace
LEFT JOIN pg_catalog.pg_type AS base_type ON base_type.oid = t.typbasetype
LEFT JOIN pg_catalog.pg_namespace AS base_ns ON base_ns.oid = base_type.typnamespace
LEFT JOIN pg_catalog.pg_attrdef AS default_value
  ON default_value.adrelid = a.attrelid AND default_value.adnum = a.attnum
WHERE a.attrelid = $1
  AND a.attnum > 0
  AND NOT a.attisdropped
  AND (
    pg_catalog.has_table_privilege(a.attrelid, 'SELECT')
    OR pg_catalog.has_column_privilege(a.attrelid, a.attnum, 'SELECT')
  )
  AND ($2 = '' OR pg_catalog.strpos(pg_catalog.lower(a.attname), pg_catalog.lower($2)) > 0)
  AND a.attnum > $3
ORDER BY a.attnum
LIMIT $5`
)

//nolint:gocyclo // Column discovery validates access, maps metadata, and attaches constraints in one bounded call.
func (d *Discoverer) ListColumns(
	ctx context.Context,
	request execution.ColumnDiscoveryRequest,
) (execution.ColumnDiscoveryPage, error) {
	client, err := d.open(ctx, request.SourceID)
	if err != nil {
		return execution.ColumnDiscoveryPage{}, err
	}

	queryCtx, cancel := d.queryContext(ctx)
	defer cancel()

	if err := checkSchema(
		ctx,
		queryCtx,
		client.pool,
		request.Schema,
	); err != nil {
		return execution.ColumnDiscoveryPage{}, err
	}

	relationOID, relationKindValue, err := resolveRelation(
		ctx,
		queryCtx,
		client.pool,
		request.Schema,
		request.Table,
	)
	if err != nil {
		return execution.ColumnDiscoveryPage{}, err
	}

	rows, err := client.pool.Query(
		queryCtx,
		listColumnsSQL,
		relationOID,
		request.Search,
		request.AfterOrdinal,
		request.IncludeDescriptions,
		request.Limit+1,
	)
	if err != nil {
		return execution.ColumnDiscoveryPage{}, classifyQueryError(ctx, queryCtx, err)
	}

	if rows == nil {
		return execution.ColumnDiscoveryPage{}, fmt.Errorf("%w: nil catalog rows", execution.ErrInternal)
	}
	defer rows.Close()

	columns := make([]execution.Column, 0, request.Limit+1)
	for rows.Next() {
		column, err := scanColumn(rows, request.IncludeDescriptions)
		if err != nil {
			return execution.ColumnDiscoveryPage{}, classifyQueryError(ctx, queryCtx, err)
		}

		columns = append(columns, column)
	}

	if err := rows.Err(); err != nil {
		return execution.ColumnDiscoveryPage{}, classifyQueryError(ctx, queryCtx, err)
	}

	page := execution.ColumnDiscoveryPage{
		Columns:      columns,
		RelationKind: relationKindValue,
		Constraints:  make([]execution.Constraint, 0),
	}
	if len(page.Columns) > request.Limit {
		page.HasMore = true
		page.Columns = page.Columns[:request.Limit]
	}

	attnums := make([]int16, 0, len(page.Columns))
	for _, column := range page.Columns {
		if column.OrdinalPosition <= 0 || column.OrdinalPosition > math.MaxInt16 {
			return execution.ColumnDiscoveryPage{}, fmt.Errorf("%w: invalid column ordinal", execution.ErrInternal)
		}

		attnums = append(attnums, int16(column.OrdinalPosition))
	}

	page.Constraints, err = listConstraints(
		ctx,
		queryCtx,
		client.pool,
		relationOID,
		attnums,
	)
	if err != nil {
		return execution.ColumnDiscoveryPage{}, err
	}

	return page, nil
}

func resolveRelation(
	parentCtx context.Context,
	queryCtx context.Context,
	pool runtimePool,
	schema string,
	table string,
) (uint32, execution.RelationKind, error) {
	rows, err := pool.Query(queryCtx, resolveRelationSQL, schema, table)
	if err != nil {
		return 0, "", classifyQueryError(parentCtx, queryCtx, err)
	}

	if rows == nil {
		return 0, "", fmt.Errorf("%w: nil relation rows", execution.ErrInternal)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, "", classifyQueryError(parentCtx, queryCtx, err)
		}

		return 0, "", execution.ErrRelationNotFound
	}

	var (
		oid          uint32
		relationCode string
		readable     bool
	)
	if err := rows.Scan(&oid, &relationCode, &readable); err != nil {
		return 0, "", classifyQueryError(parentCtx, queryCtx, err)
	}

	if err := rows.Err(); err != nil {
		return 0, "", classifyQueryError(parentCtx, queryCtx, err)
	}

	if !readable {
		return 0, "", execution.ErrDatabasePermissionDenied
	}

	kind, err := relationKind(relationCode)
	if err != nil {
		return 0, "", err
	}

	return oid, kind, nil
}

func scanColumn(rows catalogRows, includeDescriptions bool) (execution.Column, error) {
	var (
		columnName        string
		ordinal           int
		formattedType     string
		typeSchema        string
		typeName          string
		categoryCode      string
		length            *int32
		precision         *int32
		scale             *int32
		temporal          *int32
		isArray           bool
		elementSchema     *string
		elementName       *string
		baseSchema        *string
		baseName          *string
		nullable          bool
		defaultExpression *string
		identityCode      string
		generatedCode     string
		description       *string
	)
	if err := rows.Scan(
		&columnName,
		&ordinal,
		&formattedType,
		&typeSchema,
		&typeName,
		&categoryCode,
		&length,
		&precision,
		&scale,
		&temporal,
		&isArray,
		&elementSchema,
		&elementName,
		&baseSchema,
		&baseName,
		&nullable,
		&defaultExpression,
		&identityCode,
		&generatedCode,
		&description,
	); err != nil {
		return execution.Column{}, err
	}

	if ordinal <= 0 || ordinal > math.MaxInt16 {
		return execution.Column{}, fmt.Errorf("%w: invalid column ordinal", execution.ErrInternal)
	}

	category, err := typeCategory(categoryCode)
	if err != nil {
		return execution.Column{}, err
	}

	dataType := execution.DataType{
		Schema:            typeSchema,
		Name:              typeName,
		Category:          category,
		Length:            length,
		Precision:         precision,
		Scale:             scale,
		TemporalPrecision: temporal,
		IsArray:           isArray,
	}
	if elementSchema != nil && elementName != nil {
		dataType.ElementType = &execution.TypeReference{Schema: *elementSchema, Name: *elementName}
	}

	if baseSchema != nil && baseName != nil {
		dataType.DomainBaseType = &execution.TypeReference{Schema: *baseSchema, Name: *baseName}
	}

	identity, err := columnIdentity(identityCode)
	if err != nil {
		return execution.Column{}, err
	}

	generated, err := columnGenerated(generatedCode, defaultExpression)
	if err != nil {
		return execution.Column{}, err
	}

	if generated != nil {
		defaultExpression = nil
	}

	if !includeDescriptions {
		description = nil
	}

	return execution.Column{
		Name:              columnName,
		OrdinalPosition:   ordinal,
		FormattedType:     formattedType,
		Type:              dataType,
		Nullable:          nullable,
		DefaultExpression: defaultExpression,
		Identity:          identity,
		Generated:         generated,
		Description:       description,
	}, nil
}

func typeCategory(code string) (execution.TypeCategory, error) {
	switch code {
	case typeCategoryBaseCode:
		return execution.TypeCategoryBase, nil
	case "array":
		return execution.TypeCategoryArray, nil
	case "domain":
		return execution.TypeCategoryDomain, nil
	case "enum":
		return execution.TypeCategoryEnum, nil
	case "composite":
		return execution.TypeCategoryComposite, nil
	case "range":
		return execution.TypeCategoryRange, nil
	case "multirange":
		return execution.TypeCategoryMultirange, nil
	case "pseudo":
		return execution.TypeCategoryPseudo, nil
	case "other":
		return execution.TypeCategoryOther, nil
	default:
		return "", fmt.Errorf("%w: unknown type category", execution.ErrInternal)
	}
}

func columnIdentity(code string) (*execution.Identity, error) {
	switch code {
	case "":
		return nil, nil
	case "a":
		return &execution.Identity{Generation: "always"}, nil
	case "d":
		return &execution.Identity{Generation: "by_default"}, nil
	default:
		return nil, fmt.Errorf("%w: unknown identity code", execution.ErrInternal)
	}
}

func columnGenerated(code string, expression *string) (*execution.Generated, error) {
	switch code {
	case "":
		return nil, nil
	case "s", "v":
		value := ""
		if expression != nil {
			value = *expression
		}

		kind := "stored"
		if code == "v" {
			kind = "virtual"
		}

		return &execution.Generated{Kind: kind, Expression: value}, nil
	default:
		return nil, fmt.Errorf("%w: unknown generated code", execution.ErrInternal)
	}
}
