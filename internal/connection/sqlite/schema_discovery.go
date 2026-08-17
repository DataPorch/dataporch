package sqlite

import (
	"context"
	"errors"
	"strings"

	"github.com/adamraziv/dataporch/internal/execution"
)

func (d *Discoverer) ListSchemas(
	ctx context.Context,
	request execution.SchemaDiscoveryRequest,
) (page execution.SchemaDiscoveryPage, retErr error) {
	client, err := d.open(ctx, request.SourceID)
	if err != nil {
		return execution.SchemaDiscoveryPage{}, err
	}
	defer func() { retErr = errors.Join(retErr, client.close()) }()

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
