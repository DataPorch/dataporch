package postgres

import (
	"context"
	"fmt"

	"github.com/adamraziv/dataporch/internal/execution"
)

const listSchemasSQL = `
SELECT
    n.nspname,
    CASE WHEN $1 THEN pg_catalog.obj_description(n.oid, 'pg_namespace') END
FROM pg_catalog.pg_namespace AS n
WHERE pg_catalog.has_schema_privilege(n.oid, 'USAGE')
  AND ($2 = '' OR pg_catalog.strpos(pg_catalog.lower(n.nspname), pg_catalog.lower($2)) > 0)
  AND ($3 = '' OR n.nspname COLLATE "C" > $3 COLLATE "C")
ORDER BY n.nspname COLLATE "C"
LIMIT $4`

func (d *Discoverer) ListSchemas(
	ctx context.Context,
	request execution.SchemaDiscoveryRequest,
) (execution.SchemaDiscoveryPage, error) {
	client, err := d.open(ctx, request.SourceID)
	if err != nil {
		return execution.SchemaDiscoveryPage{}, err
	}

	queryCtx, cancel := d.queryContext(ctx)
	defer cancel()

	rows, err := client.pool.Query(
		queryCtx,
		listSchemasSQL,
		request.IncludeDescriptions,
		request.Search,
		request.AfterName,
		request.Limit+1,
	)
	if err != nil {
		return execution.SchemaDiscoveryPage{}, classifyQueryError(ctx, queryCtx, err)
	}

	if rows == nil {
		return execution.SchemaDiscoveryPage{}, fmt.Errorf("%w: nil catalog rows", execution.ErrInternal)
	}
	defer rows.Close()

	schemas := make([]execution.Schema, 0, request.Limit+1)

	for rows.Next() {
		var schema execution.Schema
		if err := rows.Scan(&schema.Name, &schema.Description); err != nil {
			return execution.SchemaDiscoveryPage{}, classifyQueryError(ctx, queryCtx, err)
		}

		if !request.IncludeDescriptions {
			schema.Description = nil
		}

		schemas = append(schemas, schema)
	}

	if err := rows.Err(); err != nil {
		return execution.SchemaDiscoveryPage{}, classifyQueryError(ctx, queryCtx, err)
	}

	page := execution.SchemaDiscoveryPage{Schemas: schemas}
	if len(page.Schemas) > request.Limit {
		page.HasMore = true
		page.Schemas = page.Schemas[:request.Limit]
	}

	return page, nil
}
