package app

import (
	"errors"

	"github.com/adamraziv/dataporch/internal/connection"
)

var errDefinitionRegistrarRequired = errors.New("app: definition registrar is required")

type replacementRegistrar struct {
	registrar connection.DefinitionRegistrar
	runtimes  map[connection.Kind]runtimeLifecycle
}

var _ connection.DefinitionRegistrar = (*replacementRegistrar)(nil)

func newReplacementRegistrar(
	registrar connection.DefinitionRegistrar,
	runtimes map[connection.Kind]runtimeLifecycle,
) (*replacementRegistrar, error) {
	if isNilDependency(registrar) {
		return nil, errDefinitionRegistrarRequired
	}

	owned := make(map[connection.Kind]runtimeLifecycle, len(runtimes))
	for kind, runtime := range runtimes {
		owned[kind] = runtime
	}

	return &replacementRegistrar{registrar: registrar, runtimes: owned}, nil
}

func (r *replacementRegistrar) Register(
	definition connection.Definition,
) (connection.RegistrationResult, error) {
	result, err := r.registrar.Register(definition)
	if err != nil {
		return connection.RegistrationResult{}, err
	}

	if result.Replaced {
		r.invalidate(result.Previous.Kind, definition.ID)
	}
	if !result.Replaced || result.Previous.Kind != definition.Kind {
		r.invalidate(definition.Kind, definition.ID)
	}

	return result, nil
}

func (r *replacementRegistrar) invalidate(kind connection.Kind, id connection.ID) {
	runtime, exists := r.runtimes[kind]
	if !exists || isNilDependency(runtime) {
		return
	}

	runtime.Invalidate(id)
}
