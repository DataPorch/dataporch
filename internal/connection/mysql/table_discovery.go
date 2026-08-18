package mysql

import (
	"context"
	"errors"
	"fmt"

	"github.com/adamraziv/dataporch/internal/execution"
)

const listTablesSQL = `
SELECT
    TABLE_NAME,
    TABLE_TYPE,
    CASE WHEN ? THEN NULLIF(TABLE_COMMENT, '') END
FROM INFORMATION_SCHEMA.TABLES
WHERE TABLE_SCHEMA = ?
  AND TABLE_TYPE IN ('BASE TABLE', 'VIEW')
  AND (? = '' OR LOCATE(LOWER(?), LOWER(TABLE_NAME)) > 0)
  AND (? = '' OR CAST(TABLE_NAME AS BINARY) > CAST(? AS BINARY))
ORDER BY CAST(TABLE_NAME AS BINARY)
LIMIT ?`

func (d *Discoverer) ListTables(
	ctx context.Context,
	request execution.TableDiscoveryRequest,
) (page execution.TableDiscoveryPage, retErr error) {
	client, err := d.open(ctx, request.SourceID)
	if err != nil {
		return page, err
	}
	if request.Schema != client.database {
		return page, execution.ErrSchemaNotFound
	}

	queryCtx, cancel := d.queryContext(ctx)
	defer cancel()

	rows, err := client.pool.Query(
		queryCtx,
		listTablesSQL,
		request.IncludeDescriptions,
		request.Schema,
		request.Search,
		request.Search,
		request.AfterName,
		request.AfterName,
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

	page.Tables = make([]execution.Table, 0, request.Limit+1)
	for rows.Next() {
		var (
			table        execution.Table
			relationType string
		)
		if err := rows.Scan(&table.Name, &relationType, &table.Description); err != nil {
			return page, classifyDiscoveryQueryError(ctx, queryCtx, err)
		}

		table.Kind, err = relationKind(relationType)
		if err != nil {
			return page, err
		}
		if !request.IncludeDescriptions {
			table.Description = nil
		}
		page.Tables = append(page.Tables, table)
	}

	if err := rows.Err(); err != nil {
		return page, classifyDiscoveryQueryError(ctx, queryCtx, err)
	}

	if len(page.Tables) > request.Limit {
		page.HasMore = true
		page.Tables = page.Tables[:request.Limit]
	}
	return page, nil
}

func relationKind(tableType string) (execution.RelationKind, error) {
	switch tableType {
	case "BASE TABLE":
		return execution.RelationKindTable, nil
	case "VIEW":
		return execution.RelationKindView, nil
	default:
		return "", fmt.Errorf("%w: %s", execution.ErrUnsupportedRelationKind, tableType)
	}
}
