package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"sync"

	"github.com/adamraziv/dataporch/internal/atomicfile"
	"github.com/adamraziv/dataporch/internal/connection"
)

var (
	ErrStoreCorrupt       = errors.New("connection filestore: store is corrupt")
	ErrInvalidPermissions = errors.New("connection filestore: invalid file permissions")
)

type Store struct {
	mu            sync.RWMutex
	path          string
	definitions   map[connection.ID]connection.Definition
	writeSnapshot func(string, []connection.Definition) error
}

type snapshot struct {
	Connections []connection.Definition `json:"connections"`
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("connection filestore: path is required")
	}

	definitions, err := load(path)
	if err != nil {
		return nil, err
	}

	return &Store{path: path, definitions: definitions, writeSnapshot: writeSnapshot}, nil
}

func (s *Store) Lookup(ctx context.Context, id connection.ID) (connection.Definition, error) {
	if err := validContext(ctx); err != nil {
		return connection.Definition{}, err
	}

	s.mu.RLock()
	definition, exists := s.definitions[id]
	s.mu.RUnlock()
	if !exists {
		return connection.Definition{}, fmt.Errorf("%w: %s", connection.ErrDefinitionNotFound, id)
	}

	return definition.Clone(), nil
}

func (s *Store) List(ctx context.Context) ([]connection.Definition, error) {
	if err := validContext(ctx); err != nil {
		return nil, err
	}

	s.mu.RLock()
	definitions := sortedDefinitions(s.definitions)
	s.mu.RUnlock()
	return definitions, nil
}

func (s *Store) Upsert(ctx context.Context, definition connection.Definition) error {
	if err := validContext(ctx); err != nil {
		return err
	}
	if err := definition.Validate(); err != nil {
		return fmt.Errorf("validating definition: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	definitions := cloneDefinitions(s.definitions)
	definitions[definition.ID] = definition.Clone()
	snapshot := sortedDefinitions(definitions)
	if err := s.writeSnapshot(s.path, snapshot); err != nil {
		return fmt.Errorf("persisting connection definitions: %w", err)
	}

	s.definitions = definitions
	return nil
}

func load(path string) (map[connection.ID]connection.Definition, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[connection.ID]connection.Definition{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stating connection store: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: not a regular file", ErrStoreCorrupt)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPermissions, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading connection store: %w", err)
	}
	var persisted snapshot
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("%w: decoding snapshot", ErrStoreCorrupt)
	}

	definitions := make(map[connection.ID]connection.Definition, len(persisted.Connections))
	for _, definition := range persisted.Connections {
		if err := definition.Validate(); err != nil {
			return nil, fmt.Errorf("%w: invalid definition", ErrStoreCorrupt)
		}
		if _, exists := definitions[definition.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate database id", ErrStoreCorrupt)
		}
		definitions[definition.ID] = definition.Clone()
	}

	return definitions, nil
}

func writeSnapshot(path string, definitions []connection.Definition) error {
	data, err := json.Marshal(snapshot{Connections: definitions})
	if err != nil {
		return fmt.Errorf("encoding connection snapshot: %w", err)
	}
	return atomicfile.Replace(path, data, 0o600)
}

func sortedDefinitions(definitions map[connection.ID]connection.Definition) []connection.Definition {
	ids := make([]string, 0, len(definitions))
	for id := range definitions {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)

	sorted := make([]connection.Definition, 0, len(ids))
	for _, id := range ids {
		sorted = append(sorted, definitions[connection.ID(id)].Clone())
	}
	return sorted
}

func cloneDefinitions(definitions map[connection.ID]connection.Definition) map[connection.ID]connection.Definition {
	cloned := make(map[connection.ID]connection.Definition, len(definitions))
	for id, definition := range definitions {
		cloned[id] = definition.Clone()
	}
	return cloned
}

func validContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("connection filestore: context is required")
	}
	return ctx.Err()
}
