package app

import (
	"context"
	"errors"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/connection/postgres"
)

var (
	errPostgresRuntimeFactoryRequired = errors.New("app: postgres runtime factory is required")
	errDefinitionRegistrarRequired    = errors.New("app: definition registrar is required")
	errRuntimeInvalidatorRequired     = errors.New("app: runtime invalidator is required")
)

type appDependencies struct {
	adapters           []connection.Adapter
	newPostgresRuntime postgresRuntimeFactory
}

type runtimeInvalidator interface {
	Invalidate(connection.ID)
}

type postgresRuntime interface {
	runtimeInvalidator
	Close(context.Context) error
}

type postgresRuntimeFactory func(postgres.DefinitionPreparer) (postgresRuntime, error)

type replacementRegistrar struct {
	registrar   connection.DefinitionRegistrar
	invalidator runtimeInvalidator
}

var _ connection.DefinitionRegistrar = (*replacementRegistrar)(nil)

func newReplacementRegistrar(
	registrar connection.DefinitionRegistrar,
	invalidator runtimeInvalidator,
) (*replacementRegistrar, error) {
	if registrar == nil {
		return nil, errDefinitionRegistrarRequired
	}

	if invalidator == nil {
		return nil, errRuntimeInvalidatorRequired
	}

	return &replacementRegistrar{registrar: registrar, invalidator: invalidator}, nil
}

func (r *replacementRegistrar) Register(definition connection.Definition) error {
	if err := r.registrar.Register(definition); err != nil {
		return err
	}

	r.invalidator.Invalidate(definition.ID)

	return nil
}

func newPostgresRuntime(preparer postgres.DefinitionPreparer) (postgresRuntime, error) {
	return postgres.NewOpener(preparer)
}
