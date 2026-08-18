package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/adamraziv/dataporch/internal/execution"
)

const listTablesSQL = `
SELECT name, type
FROM pragma_table_list
WHERE schema = 'main'
  AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
  AND type IN ('table', 'view', 'virtual')
  AND (?1 = '' OR instr(lower(name), lower(?1)) > 0)
  AND (?2 = '' OR name COLLATE BINARY > ?2 COLLATE BINARY)
ORDER BY name COLLATE BINARY
LIMIT ?3`

//nolint:gocyclo // Catalog execution keeps validation, binding, pagination, and cleanup in one ordered lifecycle.
func (d *Discoverer) ListTables(
	ctx context.Context,
	request execution.TableDiscoveryRequest,
) (page execution.TableDiscoveryPage, retErr error) {
	if ctx == nil {
		return page, fmt.Errorf("%w: context is required", execution.ErrCancelled)
	}

	client, queryCtx, cancel, err := d.openCatalog(ctx, request.SourceID)
	if err != nil {
		return execution.TableDiscoveryPage{}, err
	}
	defer cancel()
	defer func() {
		retErr = errors.Join(
			retErr,
			projectSQLiteDiscoveryError(ctx, queryCtx, client.close(), sqliteErrorPhaseClose),
		)
	}()

	page.Tables = make([]execution.Table, 0, request.Limit+1)
	if request.Schema != sqliteMainSchema {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, execution.ErrSchemaNotFound, sqliteErrorPhaseStep)
	}

	stmt, tail, err := client.conn.Prepare(listTablesSQL)
	if err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhasePrepare)
	}

	if stmt == nil || strings.TrimSpace(tail) != "" {
		invalidErr := fmt.Errorf("%w: invalid relation catalog statement", execution.ErrInternal)
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

	if err := stmt.BindText(1, request.Search); err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
	}

	if err := stmt.BindText(2, request.AfterName); err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
	}

	if err := stmt.BindInt64(3, int64(request.Limit+1)); err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
	}

	for stmt.Step() {
		table := execution.Table{
			Name: stmt.ColumnText(0),
		}

		relationType := stmt.ColumnText(1)
		switch relationType {
		case "table":
			table.Kind = execution.RelationKindTable
		case "view":
			table.Kind = execution.RelationKindView
		case sqliteRelationKindVirtual:
			table.Kind = execution.RelationKindVirtualTable
		default:
			return page, projectSQLiteDiscoveryError(ctx, queryCtx, fmt.Errorf("%w: %s", execution.ErrInternal, relationType), sqliteErrorPhaseStep)
		}

		page.Tables = append(page.Tables, table)
	}

	if err := stmt.Err(); err != nil {
		return page, projectSQLiteDiscoveryError(ctx, queryCtx, err, sqliteErrorPhaseStep)
	}

	if len(page.Tables) > request.Limit {
		page.HasMore = true
		page.Tables = page.Tables[:request.Limit]
	}

	return page, nil
}
