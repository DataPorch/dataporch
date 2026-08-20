//nolint:dupl // Relational module constructors intentionally mirror explicit adapter wiring.
package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/connection/mysql"
)

func newMySQLModule(
	manager *connection.Manager,
	policy queryPolicy,
) (relationalModule, error) {
	if manager == nil {
		return relationalModule{}, errRelationalManagerRequired
	}

	adapter := mysql.New()

	runtime, err := mysql.NewOpener(manager)
	if err != nil {
		return relationalModule{}, fmt.Errorf("creating mysql runtime: %w", err)
	}

	discoverer, err := mysql.NewDiscoverer(runtime)
	if err != nil {
		return relationalModule{}, errors.Join(
			fmt.Errorf("creating mysql discoverer: %w", err),
			runtime.Close(context.Background()),
		)
	}

	queryExecutor, err := mysql.NewQueryExecutor(runtime, mysql.QueryOptions{
		Timeout:           policy.timeout,
		ResponseByteLimit: policy.responseByteLimit,
		TruncationEnabled: policy.truncationEnabled,
		RowLimit:          policy.rowLimit,
	})
	if err != nil {
		return relationalModule{}, errors.Join(
			fmt.Errorf("creating mysql query executor: %w", err),
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
