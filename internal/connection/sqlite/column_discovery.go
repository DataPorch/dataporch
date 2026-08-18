package sqlite

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/adamraziv/dataporch/internal/execution"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

const (
	resolveRelationSQL = `
SELECT type
FROM pragma_table_list
WHERE schema = 'main'
  AND name = ?1
  AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
LIMIT 1`

	listColumnsSQL = `
SELECT cid, name, type, "notnull", dflt_value, pk, hidden
FROM pragma_table_xinfo(?1)
WHERE hidden != 1
  AND (?2 = '' OR instr(lower(name), lower(?2)) > 0)
  AND cid + 1 > ?3
ORDER BY cid
LIMIT ?4`
)

func typeAffinity(declared string) execution.TypeAffinity {
	upper := strings.ToUpper(declared)
	switch {
	case strings.Contains(upper, "INT"):
		return execution.TypeAffinityInteger
	case strings.Contains(upper, "CHAR"),
		strings.Contains(upper, "CLOB"),
		strings.Contains(upper, "TEXT"):
		return execution.TypeAffinityText
	case upper == "", strings.Contains(upper, "BLOB"):
		return execution.TypeAffinityBlob
	case strings.Contains(upper, "REAL"),
		strings.Contains(upper, "FLOA"),
		strings.Contains(upper, "DOUB"):
		return execution.TypeAffinityReal
	default:
		return execution.TypeAffinityNumeric
	}
}

func (d *Discoverer) ListColumns(
	ctx context.Context,
	request execution.ColumnDiscoveryRequest,
) (page execution.ColumnDiscoveryPage, retErr error) {
	if ctx == nil {
		return page, fmt.Errorf("%w: context is required", execution.ErrCancelled)
	}

	queryCtx, cancel := d.queryContext(ctx)
	defer cancel()

	client, err := d.open(queryCtx, request.SourceID)
	if err != nil {
		return execution.ColumnDiscoveryPage{}, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseOpen)
	}
	defer func() {
		retErr = errors.Join(
			retErr,
			projectSQLiteDiscoveryError(ctx, queryCtx, client.close(), sqliteErrorPhaseClose),
		)
	}()

	page.Columns = make([]execution.Column, 0, request.Limit+1)
	page.Constraints = make([]execution.Constraint, 0)
	if request.Schema != "main" {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, execution.ErrSchemaNotFound, sqliteErrorPhaseStep)
	}

	page.RelationKind, err = resolveRelationKind(client.conn, request.Table)
	if err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
	}

	stmt, tail, err := client.conn.Prepare(listColumnsSQL)
	if err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhasePrepare)
	}
	if stmt == nil || strings.TrimSpace(tail) != "" {
		invalidErr := fmt.Errorf("%w: invalid column catalog statement", execution.ErrInternal)
		if stmt != nil {
			invalidErr = errors.Join(
				invalidErr,
				projectSQLiteDiscoveryError(ctx, queryCtx, stmt.Close(), sqliteErrorPhaseClose),
			)
		}
		return page, invalidErr
	}
	defer func() {
		retErr = errors.Join(
			retErr,
			projectSQLiteDiscoveryError(ctx, queryCtx, stmt.Close(), sqliteErrorPhaseClose),
		)
	}()

	if err := stmt.BindText(1, request.Table); err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
	}
	if err := stmt.BindText(2, request.Search); err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
	}
	if err := stmt.BindInt64(3, int64(request.AfterOrdinal)); err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
	}
	if err := stmt.BindInt64(4, int64(request.Limit+1)); err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
	}

	for stmt.Step() {
		column, err := scanSQLiteColumn(stmt)
		if err != nil {
			return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
		}
		page.Columns = append(page.Columns, column)
	}
	if err := stmt.Err(); err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
	}

	if len(page.Columns) > request.Limit {
		page.HasMore = true
		page.Columns = page.Columns[:request.Limit]
	}
	if request.AfterOrdinal == 0 {
		page.Constraints, err = listSQLiteConstraints(client.conn, request.Table)
		if err != nil {
			return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
		}
	}

	return page, nil
}

func resolveRelationKind(conn rawConnection, table string) (kind execution.RelationKind, retErr error) {
	stmt, tail, err := conn.Prepare(resolveRelationSQL)
	if err != nil {
		return "", fmt.Errorf("sqlite: preparing relation lookup: %w", err)
	}
	if stmt == nil || strings.TrimSpace(tail) != "" {
		invalidErr := fmt.Errorf("%w: invalid relation lookup statement", execution.ErrInternal)
		if stmt != nil {
			invalidErr = errors.Join(invalidErr, stmt.Close())
		}
		return "", invalidErr
	}
	defer func() { retErr = errors.Join(retErr, stmt.Close()) }()

	if err := stmt.BindText(1, table); err != nil {
		return "", fmt.Errorf("sqlite: binding relation lookup: %w", err)
	}
	if !stmt.Step() {
		if err := stmt.Err(); err != nil {
			return "", fmt.Errorf("sqlite: reading relation lookup: %w", err)
		}
		return "", execution.ErrRelationNotFound
	}
	if err := stmt.Err(); err != nil {
		return "", fmt.Errorf("sqlite: reading relation lookup: %w", err)
	}

	switch relationType := stmt.ColumnText(0); relationType {
	case "table":
		return execution.RelationKindTable, nil
	case "view":
		return execution.RelationKindView, nil
	case "virtual":
		return execution.RelationKindVirtualTable, nil
	case "shadow":
		return "", execution.ErrRelationNotFound
	default:
		return "", fmt.Errorf("%w: %s", execution.ErrUnsupportedRelationKind, relationType)
	}
}

func scanSQLiteColumn(stmt statement) (execution.Column, error) {
	cid := stmt.ColumnInt64(0)
	if cid < 0 || cid >= math.MaxInt64 {
		return execution.Column{}, fmt.Errorf("%w: invalid column ordinal", execution.ErrInternal)
	}
	ordinal := cid + 1
	if ordinal > int64(maxInt()) {
		return execution.Column{}, fmt.Errorf("%w: invalid column ordinal", execution.ErrInternal)
	}

	declared := stmt.ColumnText(2)
	column := execution.Column{
		Name:            stmt.ColumnText(1),
		OrdinalPosition: int(ordinal),
		FormattedType:   declared,
		Type: execution.DataType{
			Category: execution.TypeCategoryDynamic,
			Affinity: typeAffinity(declared),
		},
		Nullable: stmt.ColumnInt64(3) == 0,
	}
	if stmt.ColumnType(4) != sqlite3.NULL {
		defaultExpression := stmt.ColumnText(4)
		column.DefaultExpression = &defaultExpression
	}

	switch hidden := stmt.ColumnInt64(6); hidden {
	case 0:
	case 2:
		column.Generated = &execution.Generated{Kind: "virtual"}
	case 3:
		column.Generated = &execution.Generated{Kind: "stored"}
	default:
		return execution.Column{}, fmt.Errorf("%w: unknown sqlite hidden column value %d", execution.ErrInternal, hidden)
	}

	return column, nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
