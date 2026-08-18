//nolint:dupl // Relational module constructors intentionally mirror explicit adapter wiring.
package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/connection/postgres"
)

func newPostgresModule(
	manager *connection.Manager,
	policy queryPolicy,
) (relationalModule, error) {
	if manager == nil {
		return relationalModule{}, errRelationalManagerRequired
	}

	adapter := postgres.New()

	runtime, err := postgres.NewOpener(manager)
	if err != nil {
		return relationalModule{}, fmt.Errorf("creating postgres runtime: %w", err)
	}

	discoverer, err := postgres.NewDiscoverer(runtime)
	if err != nil {
		return relationalModule{}, errors.Join(
			fmt.Errorf("creating postgres discoverer: %w", err),
			runtime.Close(context.Background()),
		)
	}

	queryExecutor, err := postgres.NewQueryExecutor(runtime, postgres.QueryOptions{
		Timeout:           policy.timeout,
		ResponseByteLimit: policy.responseByteLimit,
		TruncationEnabled: policy.truncationEnabled,
		RowLimit:          policy.rowLimit,
	})
	if err != nil {
		return relationalModule{}, errors.Join(
			fmt.Errorf("creating postgres query executor: %w", err),
			runtime.Close(context.Background()),
		)
	}

	return relationalModule{
		adapter:       adapter,
		discoverer:    discoverer,
		queryExecutor: queryExecutor,
		runtime:       runtime,
	}, nil
}
