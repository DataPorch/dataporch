package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/DataPorch/dataporch/internal/connection"
)

var (
	ErrUnsupportedKind = errors.New("postgres: unsupported database kind")
	ErrOpenTimeout     = errors.New("postgres: open timeout")
	ErrRuntimeClosed   = errors.New("postgres: runtime closed")
	ErrShutdownTimeout = errors.New("postgres: shutdown timeout")

	errDefinitionPreparerRequired = errors.New("postgres: definition preparer is required")
	errPoolFactoryRequired        = errors.New("postgres: pool factory is required")
	errOpenTimeoutRequired        = errors.New("postgres: open timeout is required")
	errOpenInvalidated            = errors.New("postgres: open invalidated")
	errRuntimeContextRequired     = errors.New("postgres: context is required")
	errRuntimeInvalidID           = errors.New("postgres: invalid database id")
	errRuntimeDefinitionMismatch  = errors.New("postgres: runtime definition mismatch")
	errRuntimePoolMissing         = errors.New("postgres: runtime pool is missing")
)

// DefinitionPreparer resolves the current saved definition and its secrets.
type DefinitionPreparer interface {
	Prepare(context.Context, connection.ID) (connection.ResolvedDefinition, error)
}

// Client is a cached Postgres runtime handle reserved for internal capabilities.
type Client struct {
	pool runtimePool
}

type openAttempt struct {
	done   chan struct{}
	cancel context.CancelCauseFunc
	client *Client
	err    error
}

type cacheEntry struct {
	generation uint64
	client     *Client
	attempt    *openAttempt
}

type openerDependencies struct {
	preparer    DefinitionPreparer
	pools       poolFactory
	openTimeout time.Duration
}

// Opener lazily authenticates and caches one Postgres runtime per database ID.
type Opener struct {
	mu          sync.Mutex
	preparer    DefinitionPreparer
	pools       poolFactory
	openTimeout time.Duration
	entries     map[connection.ID]*cacheEntry
	generations map[connection.ID]uint64
	attempts    sync.WaitGroup
	closers     sync.WaitGroup
	closeDone   chan struct{}
	isClosed    bool
}

var _ DefinitionPreparer = (*connection.Manager)(nil)

// NewOpener constructs an opener without contacting PostgreSQL.
func NewOpener(preparer DefinitionPreparer) (*Opener, error) {
	if preparer == nil {
		return nil, errDefinitionPreparerRequired
	}

	pools, err := newPGXPoolFactory()
	if err != nil {
		return nil, fmt.Errorf("creating postgres pool factory: %w", err)
	}

	return newOpener(openerDependencies{
		preparer:    preparer,
		pools:       pools,
		openTimeout: initialOpenTimeout,
	})
}

func newOpener(dependencies openerDependencies) (*Opener, error) {
	if dependencies.preparer == nil {
		return nil, errDefinitionPreparerRequired
	}

	if dependencies.pools == nil {
		return nil, errPoolFactoryRequired
	}

	if dependencies.openTimeout <= 0 {
		return nil, errOpenTimeoutRequired
	}

	return &Opener{
		preparer:    dependencies.preparer,
		pools:       dependencies.pools,
		openTimeout: dependencies.openTimeout,
		entries:     make(map[connection.ID]*cacheEntry),
		generations: make(map[connection.ID]uint64),
		closeDone:   make(chan struct{}),
	}, nil
}

// Open returns a cached runtime or authenticates one shared opening attempt.
func (o *Opener) Open(ctx context.Context, id connection.ID) (*Client, error) {
	client, err := o.openRaw(ctx, id)
	if err != nil {
		return nil, o.discoveryOpenError(ctx, id, err)
	}

	return client, nil
}

func (o *Opener) OpenQuery(ctx context.Context, id connection.ID) (*Client, error) {
	client, err := o.openRaw(ctx, id)
	if err != nil {
		return nil, projectRelationalQueryError(err)
	}

	return client, nil
}

func (o *Opener) openRaw(ctx context.Context, id connection.ID) (*Client, error) {
	if ctx == nil {
		return nil, errRuntimeContextRequired
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := (connection.Definition{ID: id, Kind: Kind}).Validate(); err != nil {
		return nil, errRuntimeInvalidID
	}

	o.mu.Lock()
	if o.isClosed {
		o.mu.Unlock()
		return nil, ErrRuntimeClosed
	}

	generation := o.generations[id]

	entry := o.entries[id]
	if entry != nil {
		if entry.client != nil {
			client := entry.client
			o.mu.Unlock()

			return client, nil
		}

		if entry.attempt != nil {
			attempt := entry.attempt
			o.mu.Unlock()

			return waitForAttempt(ctx, attempt)
		}
	}

	sharedCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	attemptCtx, stop := context.WithTimeoutCause(sharedCtx, o.openTimeout, ErrOpenTimeout)
	attempt := &openAttempt{done: make(chan struct{}), cancel: cancel}
	o.entries[id] = &cacheEntry{generation: generation, attempt: attempt}
	o.attempts.Add(1)
	o.mu.Unlock()

	go o.runAttempt(attemptCtx, stop, id, generation, attempt)

	return waitForAttempt(ctx, attempt)
}

func (o *Opener) runAttempt(
	ctx context.Context,
	stop context.CancelFunc,
	id connection.ID,
	generation uint64,
	attempt *openAttempt,
) {
	defer o.attempts.Done()
	defer stop()

	client, err := o.openRuntime(ctx, id)
	o.finishAttempt(id, generation, attempt, client, err)
}

func (o *Opener) openRuntime(ctx context.Context, id connection.ID) (*Client, error) {
	resolved, err := o.preparer.Prepare(ctx, id)
	if err != nil {
		return nil, rawAttemptError(ctx, err)
	}
	defer clearResolvedSecrets(resolved.Secrets)

	if resolved.ID != id {
		return nil, errRuntimeDefinitionMismatch
	}

	if resolved.Kind != Kind {
		return nil, ErrUnsupportedKind
	}

	pool, err := o.pools.New(ctx, resolved)
	if err != nil {
		return nil, rawAttemptError(ctx, err)
	}

	if pool == nil {
		return nil, errRuntimePoolMissing
	}

	if err := pool.Ping(ctx); err != nil {
		o.closeTracked(pool)
		return nil, rawAttemptError(ctx, err)
	}

	if context.Cause(ctx) != nil {
		o.closeTracked(pool)
		return nil, context.Cause(ctx)
	}

	return &Client{pool: pool}, nil
}

func rawAttemptError(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}

	return err
}

func (o *Opener) finishAttempt(
	id connection.ID,
	generation uint64,
	attempt *openAttempt,
	client *Client,
	err error,
) {
	var stalePool runtimePool

	o.mu.Lock()
	entry, exists := o.entries[id]
	isCurrent := !o.isClosed &&
		o.generations[id] == generation &&
		exists &&
		entry != nil &&
		entry.attempt == attempt

	if isCurrent {
		o.publishAttemptLocked(id, entry, attempt, client, err)
	} else {
		stalePool = o.rejectAttemptLocked(id, attempt, client, err)
	}

	close(attempt.done)
	o.mu.Unlock()

	if stalePool != nil {
		o.closeTracked(stalePool)
	}
}

func (o *Opener) publishAttemptLocked(
	id connection.ID,
	entry *cacheEntry,
	attempt *openAttempt,
	client *Client,
	err error,
) {
	if err == nil && client != nil {
		entry.client = client
		entry.attempt = nil
		attempt.client = client

		return
	}

	delete(o.entries, id)

	if err == nil {
		err = errRuntimePoolMissing
	}

	attempt.err = err
}

func (o *Opener) rejectAttemptLocked(
	id connection.ID,
	attempt *openAttempt,
	client *Client,
	err error,
) runtimePool {
	var stalePool runtimePool
	if client != nil {
		stalePool = client.pool
	}

	switch {
	case o.isClosed:
		attempt.err = ErrRuntimeClosed
	case err != nil:
		attempt.err = err
	default:
		attempt.err = errRuntimePoolMissing
	}

	return stalePool
}

func waitForAttempt(ctx context.Context, attempt *openAttempt) (*Client, error) {
	select {
	case <-attempt.done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if attempt.client != nil {
			return attempt.client, nil
		}

		return nil, attempt.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (o *Opener) discoveryOpenError(ctx context.Context, id connection.ID, err error) error {
	if ctx == nil {
		return unavailableContextError(nil)
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return unavailableContextError(ctxErr)
	}

	switch {
	case errors.Is(err, ErrOpenTimeout):
		return classifiedError(id, ErrOpenTimeout)
	case errors.Is(err, ErrUnsupportedKind):
		return classifiedError(id, ErrUnsupportedKind)
	case errors.Is(err, ErrRuntimeClosed):
		return fmt.Errorf("%w: %s", ErrRuntimeClosed, id)
	case errors.Is(err, errOpenInvalidated):
		return unavailableError(id)
	case errors.Is(err, connection.ErrDatabaseNotFound):
		return classifiedError(id, connection.ErrDatabaseNotFound)
	default:
		return unavailableError(id)
	}
}

// Invalidate detaches an ID's current generation and closes its pool asynchronously.
func (o *Opener) Invalidate(id connection.ID) {
	var closePool runtimePool

	o.mu.Lock()
	if o.isClosed {
		o.mu.Unlock()
		return
	}

	o.generations[id]++
	entry := o.entries[id]
	delete(o.entries, id)

	if entry != nil {
		if entry.attempt != nil {
			entry.attempt.cancel(errOpenInvalidated)
		}

		if entry.client != nil {
			closePool = entry.client.pool

			o.closers.Add(1)
		}
	}
	o.mu.Unlock()

	if closePool != nil {
		o.startTrackedClose(closePool)
	}
}

func (o *Opener) closeTracked(pool runtimePool) {
	if pool == nil {
		return
	}

	o.mu.Lock()
	o.closers.Add(1)
	o.mu.Unlock()

	o.startTrackedClose(pool)
}

func (o *Opener) startTrackedClose(pool runtimePool) {
	go func() {
		defer o.closers.Done()

		pool.Close()
	}()
}

// Close rejects new opens and waits for tracked runtime resources within ctx.
func (o *Opener) Close(ctx context.Context) error {
	if ctx == nil {
		return shutdownContextError(nil)
	}

	select {
	case <-o.closeDone:
		return nil
	default:
	}

	var closePools []runtimePool

	o.mu.Lock()
	if !o.isClosed {
		o.isClosed = true
		for id, entry := range o.entries {
			if entry.attempt != nil {
				entry.attempt.cancel(ErrRuntimeClosed)
			}

			if entry.client != nil {
				o.closers.Add(1)

				closePools = append(closePools, entry.client.pool)
			}

			delete(o.entries, id)
		}

		go o.completeClose()
	}
	o.mu.Unlock()

	for _, pool := range closePools {
		go func(pool runtimePool) {
			defer o.closers.Done()

			pool.Close()
		}(pool)
	}

	select {
	case <-o.closeDone:
		return nil
	case <-ctx.Done():
		select {
		case <-o.closeDone:
			return nil
		default:
		}

		return shutdownContextError(ctx.Err())
	}
}

func (o *Opener) completeClose() {
	o.attempts.Wait()
	o.closers.Wait()
	close(o.closeDone)
}

func unavailableError(id connection.ID) error {
	return fmt.Errorf("%w: %s", connection.ErrDatabaseUnavailable, id)
}

func classifiedError(id connection.ID, specific error) error {
	return fmt.Errorf("%w: %s: %w", connection.ErrDatabaseUnavailable, id, specific)
}

func unavailableContextError(err error) error {
	if err == nil {
		return fmt.Errorf("%w: context is required", connection.ErrDatabaseUnavailable)
	}

	return fmt.Errorf("%w: %w", connection.ErrDatabaseUnavailable, err)
}

func shutdownContextError(err error) error {
	if err == nil {
		return fmt.Errorf("%w: context is required", ErrShutdownTimeout)
	}

	return fmt.Errorf("%w: %w: %w", connection.ErrDatabaseUnavailable, ErrShutdownTimeout, err)
}

func clearResolvedSecrets(secrets map[string][]byte) {
	for _, value := range secrets {
		clear(value)
	}
}
