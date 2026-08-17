package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/connection/postgres"
)

var (
	errPostgresRuntimeFactoryRequired = errors.New("app: postgres runtime factory is required")
)

type appDependencies struct {
	adapters           []connection.Adapter
	newPostgresRuntime postgresRuntimeFactory
}

type postgresRuntime interface {
	Open(context.Context, connection.ID) (*postgres.Client, error)
	OpenQuery(context.Context, connection.ID) (*postgres.Client, error)
	runtimeLifecycle
	Close(context.Context) error
}

type postgresRuntimeFactory func(postgres.DefinitionPreparer) (postgresRuntime, error)

func newPostgresRuntime(preparer postgres.DefinitionPreparer) (postgresRuntime, error) {
	return postgres.NewOpener(preparer)
}

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
