package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/adamraziv/dataporch/internal/execution"
)

func (d *Discoverer) ListSchemas(
	ctx context.Context,
	request execution.SchemaDiscoveryRequest,
) (page execution.SchemaDiscoveryPage, retErr error) {
	if ctx == nil {
		return page, fmt.Errorf("%w: context is required", execution.ErrCancelled)
	}

	client, err := d.open(ctx, request.SourceID)
	if err != nil {
		return execution.SchemaDiscoveryPage{}, projectSQLiteDiscoveryError(ctx, nil, err, sqliteErrorPhaseOpen)
	}
	defer func() {
		retErr = errors.Join(
			retErr,
			projectSQLiteDiscoveryError(ctx, nil, client.close(), sqliteErrorPhaseClose),
		)
	}()

	page.Schemas = make([]execution.Schema, 0, 1)
	name := "main"
	if (request.Search == "" || strings.Contains(strings.ToLower(name), strings.ToLower(request.Search))) &&
		(request.AfterName == "" || name > request.AfterName) && request.Limit > 0 {
		page.Schemas = append(page.Schemas, execution.Schema{Name: name})
	}
	if len(page.Schemas) > request.Limit {
		page.HasMore = true
		page.Schemas = page.Schemas[:request.Limit]
	}

	return page, nil
}
