package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/connection/sqlite"
)

func newSQLiteModule(
	manager *connection.Manager,
	policy queryPolicy,
) (relationalModule, error) {
	if manager == nil {
		return relationalModule{}, errRelationalManagerRequired
	}

	runtime, err := sqlite.NewRuntime(manager)
	if err != nil {
		return relationalModule{}, fmt.Errorf("creating sqlite runtime: %w", err)
	}

	discoverer, err := sqlite.NewDiscoverer(runtime)
	if err != nil {
		return relationalModule{}, closeSQLiteModule(runtime, "creating sqlite discoverer", err)
	}

	queryExecutor, err := sqlite.NewQueryExecutor(runtime, sqlite.QueryOptions{
		Timeout:           policy.timeout,
		ResponseByteLimit: policy.responseByteLimit,
		TruncationEnabled: policy.truncationEnabled,
		RowLimit:          policy.rowLimit,
	})
	if err != nil {
		return relationalModule{}, closeSQLiteModule(runtime, "creating sqlite query executor", err)
	}

	return relationalModule{
		adapter:       sqlite.New(),
		discoverer:    discoverer,
		queryExecutor: queryExecutor,
		runtime:       runtime,
	}, nil
}

const sqliteConstructionCleanupPeriod = 2 * time.Second

func closeSQLiteModule(runtime *sqlite.Runtime, operation string, cause error) error {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		sqliteConstructionCleanupPeriod,
	)
	defer cancel()

	return errors.Join(
		fmt.Errorf("%s: %w", operation, cause),
		runtime.Close(ctx),
	)
}
