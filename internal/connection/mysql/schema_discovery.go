package mysql

import (
	"context"
	"strings"

	"github.com/adamraziv/dataporch/internal/execution"
)

func (d *Discoverer) ListSchemas(
	ctx context.Context,
	request execution.SchemaDiscoveryRequest,
) (execution.SchemaDiscoveryPage, error) {
	client, err := d.open(ctx, request.SourceID)
	if err != nil {
		return execution.SchemaDiscoveryPage{}, err
	}

	page := execution.SchemaDiscoveryPage{Schemas: make([]execution.Schema, 0, 1)}
	name := client.database
	if request.Limit <= 0 {
		return page, nil
	}
	if request.Search != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(request.Search)) {
		return page, nil
	}
	if request.AfterName != "" && name <= request.AfterName {
		return page, nil
	}

	page.Schemas = append(page.Schemas, execution.Schema{Name: name})
	return page, nil
}
