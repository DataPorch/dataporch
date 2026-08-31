package postgres

import (
	"context"
	"fmt"

	"github.com/DataPorch/dataporch/internal/execution"
)

const checkSchemaSQL = `
SELECT
    n.oid IS NOT NULL,
    COALESCE(pg_catalog.has_schema_privilege(n.oid, 'USAGE'), false)
FROM (VALUES (1)) AS marker(value)
LEFT JOIN pg_catalog.pg_namespace AS n ON n.nspname = $1`

const listTablesSQL = `
SELECT
    c.relname,
    c.relkind::text,
    CASE WHEN $2 THEN pg_catalog.obj_description(c.oid, 'pg_class') END
FROM pg_catalog.pg_class AS c
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = $1
  AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
  AND (
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
  AND ($3 = '' OR pg_catalog.strpos(pg_catalog.lower(c.relname), pg_catalog.lower($3)) > 0)
  AND ($4 = '' OR c.relname COLLATE "C" > $4 COLLATE "C")
ORDER BY c.relname COLLATE "C"
LIMIT $5`

func (d *Discoverer) ListTables(
	ctx context.Context,
	request execution.TableDiscoveryRequest,
) (execution.TableDiscoveryPage, error) {
	client, err := d.open(ctx, request.SourceID)
	if err != nil {
		return execution.TableDiscoveryPage{}, err
	}

	queryCtx, cancel := d.queryContext(ctx)
	defer cancel()

	if err := checkSchema(ctx, queryCtx, client.pool, request.Schema); err != nil {
		return execution.TableDiscoveryPage{}, err
	}

	rows, err := client.pool.Query(
		queryCtx,
		listTablesSQL,
		request.Schema,
		request.IncludeDescriptions,
		request.Search,
		request.AfterName,
		request.Limit+1,
	)
	if err != nil {
		return execution.TableDiscoveryPage{}, classifyQueryError(ctx, queryCtx, err)
	}

	if rows == nil {
		return execution.TableDiscoveryPage{}, fmt.Errorf("%w: nil catalog rows", execution.ErrInternal)
	}
	defer rows.Close()

	tables := make([]execution.Table, 0, request.Limit+1)

	for rows.Next() {
		var (
			table        execution.Table
			relationCode string
		)
		if err := rows.Scan(&table.Name, &relationCode, &table.Description); err != nil {
			return execution.TableDiscoveryPage{}, classifyQueryError(ctx, queryCtx, err)
		}

		table.Kind, err = relationKind(relationCode)
		if err != nil {
			return execution.TableDiscoveryPage{}, err
		}

		if !request.IncludeDescriptions {
			table.Description = nil
		}

		tables = append(tables, table)
	}

	if err := rows.Err(); err != nil {
		return execution.TableDiscoveryPage{}, classifyQueryError(ctx, queryCtx, err)
	}

	page := execution.TableDiscoveryPage{Tables: tables}
	if len(page.Tables) > request.Limit {
		page.HasMore = true
		page.Tables = page.Tables[:request.Limit]
	}

	return page, nil
}

func checkSchema(parentCtx, queryCtx context.Context, pool runtimePool, schema string) error {
	rows, err := pool.Query(queryCtx, checkSchemaSQL, schema)
	if err != nil {
		return classifyQueryError(parentCtx, queryCtx, err)
	}

	if rows == nil {
		return fmt.Errorf("%w: nil schema rows", execution.ErrInternal)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return classifyQueryError(parentCtx, queryCtx, err)
		}

		return execution.ErrSchemaNotFound
	}

	var (
		exists bool
		usage  bool
	)
	if err := rows.Scan(&exists, &usage); err != nil {
		return classifyQueryError(parentCtx, queryCtx, err)
	}

	if err := rows.Err(); err != nil {
		return classifyQueryError(parentCtx, queryCtx, err)
	}

	if !exists {
		return execution.ErrSchemaNotFound
	}

	if !usage {
		return execution.ErrDatabasePermissionDenied
	}

	return nil
}

func relationKind(code string) (execution.RelationKind, error) {
	switch code {
	case "r":
		return execution.RelationKindTable, nil
	case "p":
		return execution.RelationKindPartitionedTable, nil
	case "v":
		return execution.RelationKindView, nil
	case "m":
		return execution.RelationKindMaterializedView, nil
	case "f":
		return execution.RelationKindForeignTable, nil
	default:
		return "", fmt.Errorf("%w: %s", execution.ErrUnsupportedRelationKind, code)
	}
}
