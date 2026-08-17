package connection

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/adamraziv/dataporch/internal/secret"
)

var (
	ErrDatabaseUnavailable = errors.New("connection: database unavailable")
	ErrDatabaseNotFound    = errors.New("connection: database not found")
	ErrDefinitionNotFound  = errors.New("connection: definition not found")
	errResolverRequired    = errors.New("connection: secret resolver is required")
)

type SecretResolver interface {
	Resolve(context.Context, secret.Reference) ([]byte, error)
}

type Manager struct {
	mu          sync.RWMutex
	resolver    SecretResolver
	definitions map[ID]Definition
}

type RegistrationResult struct {
	Previous Definition
	Replaced bool
}

func NewManager(resolver SecretResolver, definitions []Definition) (*Manager, error) {
	if resolver == nil {
		return nil, errResolverRequired
	}

	managed := make(map[ID]Definition, len(definitions))
	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return nil, fmt.Errorf("validating definition: %w", err)
		}

		if _, exists := managed[definition.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate database id", ErrInvalidDefinition)
		}

		managed[definition.ID] = definition.Clone()
	}

	return &Manager{resolver: resolver, definitions: managed}, nil
}

func (m *Manager) Register(definition Definition) (RegistrationResult, error) {
	if err := definition.Validate(); err != nil {
		return RegistrationResult{}, fmt.Errorf("validating definition: %w", err)
	}

	next := definition.Clone()

	m.mu.Lock()
	previous, replaced := m.definitions[definition.ID]
	m.definitions[definition.ID] = next

	result := RegistrationResult{Replaced: replaced}
	if replaced {
		result.Previous = previous.Clone()
	}
	m.mu.Unlock()

	return result, nil
}

func (m *Manager) Lookup(id ID) (Definition, error) {
	m.mu.RLock()
	definition, exists := m.definitions[id]
	m.mu.RUnlock()

	if !exists {
		return Definition{}, fmt.Errorf("%w: %s", ErrDatabaseNotFound, id)
	}

	return definition.Clone(), nil
}

func (m *Manager) List() []Definition {
	m.mu.RLock()

	definitions := make([]Definition, 0, len(m.definitions))
	for _, definition := range m.definitions {
		definitions = append(definitions, definition.Clone())
	}

	m.mu.RUnlock()

	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].ID < definitions[j].ID
	})

	return definitions
}

func (m *Manager) Prepare(ctx context.Context, id ID) (ResolvedDefinition, error) {
	if ctx == nil {
		return ResolvedDefinition{}, fmt.Errorf("%w: context is required", ErrDatabaseUnavailable)
	}

	if err := ctx.Err(); err != nil {
		return ResolvedDefinition{}, fmt.Errorf("%w: %w", ErrDatabaseUnavailable, err)
	}

	definition, err := m.Lookup(id)
	if err != nil {
		if errors.Is(err, ErrDatabaseNotFound) {
			return ResolvedDefinition{}, fmt.Errorf(
				"%w: %w: %s",
				ErrDatabaseUnavailable,
				ErrDatabaseNotFound,
				id,
			)
		}

		return ResolvedDefinition{}, fmt.Errorf("%w: %s", ErrDatabaseUnavailable, id)
	}

	resolved := ResolvedDefinition{
		ID:       definition.ID,
		Kind:     definition.Kind,
		Settings: cloneStrings(definition.Settings),
		Secrets:  make(map[string][]byte, len(definition.SecretRefs)),
	}

	names := make([]string, 0, len(definition.SecretRefs))
	for name := range definition.SecretRefs {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		value, err := m.resolver.Resolve(ctx, definition.SecretRefs[name])
		if err != nil {
			clearSecrets(resolved.Secrets)
			return ResolvedDefinition{}, fmt.Errorf("%w: %s", ErrDatabaseUnavailable, id)
		}

		resolved.Secrets[name] = append([]byte(nil), value...)
	}

	return resolved, nil
}

func clearSecrets(secrets map[string][]byte) {
	for _, value := range secrets {
		zeroBytes(value)
	}
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
