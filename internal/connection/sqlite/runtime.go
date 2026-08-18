package sqlite

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/adamraziv/dataporch/internal/connection"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

var (
	errRuntimeUnavailable        = errors.New("sqlite: runtime unavailable")
	errRuntimeClosed             = errors.New("sqlite: runtime is closed")
	errInvalidResolvedDefinition = errors.New("sqlite: invalid resolved definition")
)

type DefinitionPreparer interface {
	Prepare(context.Context, connection.ID) (connection.ResolvedDefinition, error)
}

type accessMode uint8

const (
	accessModeDiscovery accessMode = iota + 1
	accessModeQuery
)

type statement interface {
	BindCount() int
	BindInt64(int, int64) error
	BindText(int, string) error
	Close() error
	ColumnCount() int
	ColumnDeclType(int) string
	ColumnFloat(int) float64
	ColumnInt64(int) int64
	ColumnName(int) string
	ColumnRawBlob(int) []byte
	ColumnRawText(int) []byte
	ColumnText(int) string
	ColumnType(int) sqlite3.Datatype
	Err() error
	Step() bool
}

type rawConnection interface {
	Close() error
	Config(sqlite3.DBConfig, ...bool) (bool, error)
	Exec(string) error
	Prepare(string) (statement, string, error)
	SetAuthorizer(func(
		sqlite3.AuthorizerActionCode,
		string,
		string,
		string,
		string,
	) sqlite3.AuthorizerReturnCode) error
	SetInterrupt(context.Context) context.Context
}

type physicalOpener func(context.Context, string, accessMode) (rawConnection, error)

type Runtime struct {
	preparer       DefinitionPreparer
	physicalOpener physicalOpener

	mu          sync.Mutex
	isClosed    bool
	active      int
	entries     map[connection.ID]*logicalEntry
	drain       chan struct{}
	drainClosed bool
}

type logicalEntry struct {
	active   int
	detached bool
}

func NewRuntime(preparer DefinitionPreparer) (*Runtime, error) {
	return newRuntime(preparer, openPhysicalConnection)
}

func newRuntime(preparer DefinitionPreparer, opener physicalOpener) (*Runtime, error) {
	if preparer == nil {
		return nil, fmt.Errorf("%w: definition preparer is required", errRuntimeUnavailable)
	}

	if opener == nil {
		return nil, fmt.Errorf("%w: physical opener is required", errRuntimeUnavailable)
	}

	return &Runtime{
		preparer:       preparer,
		physicalOpener: opener,
		entries:        make(map[connection.ID]*logicalEntry),
		drain:          make(chan struct{}),
	}, nil
}

func (r *Runtime) open(ctx context.Context, id connection.ID, mode accessMode) (*client, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", errRuntimeUnavailable)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", errRuntimeUnavailable, err)
	}

	release, err := r.acquire(id)
	if err != nil {
		return nil, err
	}

	keepLease := false
	defer func() {
		if !keepLease {
			release()
		}
	}()

	resolved, err := r.preparer.Prepare(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: preparing definition", err)
	}
	defer clearResolvedSecrets(resolved.Secrets)

	path, err := resolvedPath(id, resolved)
	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", errRuntimeUnavailable, err)
	}

	physical, err := r.physicalOpener(ctx, path, mode)
	if err != nil {
		return nil, err
	}

	if physical == nil {
		return nil, fmt.Errorf("%w: physical opener returned nil", errRuntimeUnavailable)
	}

	if err := ctx.Err(); err != nil {
		_ = physical.Close()
		return nil, fmt.Errorf("%w: %w", errRuntimeUnavailable, err)
	}

	keepLease = true

	return &client{
		conn:    physical,
		release: release,
	}, nil
}

func (r *Runtime) acquire(id connection.ID) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.isClosed {
		return nil, fmt.Errorf("%w: %w", errRuntimeUnavailable, errRuntimeClosed)
	}

	entry := r.entries[id]
	if entry == nil || entry.detached {
		entry = &logicalEntry{}
		r.entries[id] = entry
	}

	entry.active++
	r.active++

	return func() { r.release(id, entry) }, nil
}

func (r *Runtime) release(id connection.ID, entry *logicalEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry.active > 0 {
		entry.active--
	}

	if r.active > 0 {
		r.active--
	}

	if entry.active == 0 && r.entries[id] == entry {
		delete(r.entries, id)
	}

	if r.isClosed && r.active == 0 {
		r.closeDrainLocked()
	}
}

func (r *Runtime) Invalidate(id connection.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.isClosed {
		return
	}

	if entry, exists := r.entries[id]; exists {
		entry.detached = true

		delete(r.entries, id)
	}
}

func (r *Runtime) Close(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", errRuntimeUnavailable)
	}

	r.mu.Lock()
	if !r.isClosed {
		r.isClosed = true
		for id, entry := range r.entries {
			entry.detached = true

			delete(r.entries, id)
		}

		if r.active == 0 {
			r.closeDrainLocked()
		}
	}

	drain := r.drain
	r.mu.Unlock()

	select {
	case <-drain:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) closeDrainLocked() {
	if r.drainClosed {
		return
	}

	r.drainClosed = true
	close(r.drain)
}

func resolvedPath(id connection.ID, resolved connection.ResolvedDefinition) (string, error) {
	if resolved.ID != id || resolved.Kind != Kind {
		return "", fmt.Errorf("%w: identity mismatch", errInvalidResolvedDefinition)
	}

	if len(resolved.Secrets) != 1 {
		return "", fmt.Errorf("%w: path secret required", errInvalidResolvedDefinition)
	}

	pathBytes, exists := resolved.Secrets[secretPath]
	if !exists || len(pathBytes) == 0 || !utf8.Valid(pathBytes) {
		return "", fmt.Errorf("%w: path secret required", errInvalidResolvedDefinition)
	}

	if slices.Contains(pathBytes, 0) {
		return "", fmt.Errorf("%w: path secret invalid", errInvalidResolvedDefinition)
	}

	return string(pathBytes), nil
}

func clearResolvedSecrets(secrets map[string][]byte) {
	for _, value := range secrets {
		for index := range value {
			value[index] = 0
		}
	}
}

type client struct {
	conn    rawConnection
	release func()
	once    sync.Once
	err     error
}

func (c *client) close() error {
	if c == nil {
		return nil
	}

	c.once.Do(func() {
		closeErr := c.conn.Close()
		c.release()
		c.err = closeErr
	})

	return c.err
}
