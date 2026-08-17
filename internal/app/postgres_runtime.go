package app

import (
	"context"
	"errors"

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
