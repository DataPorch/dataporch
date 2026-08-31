package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DataPorch/dataporch/internal/connection"
)

var (
	ErrUnsupportedKind = errors.New("mysql: unsupported database kind")
	ErrOpenTimeout     = errors.New("mysql: open timeout")
	ErrRuntimeClosed   = errors.New("mysql: runtime closed")
	ErrShutdownTimeout = errors.New("mysql: shutdown timeout")

	errDefinitionPreparerRequired = errors.New("mysql: definition preparer is required")
	errPoolFactoryRequired        = errors.New("mysql: pool factory is required")
	errOpenTimeoutRequired        = errors.New("mysql: open timeout is required")
	errOpenInvalidated            = errors.New("mysql: open invalidated")
	errRuntimeContextRequired     = errors.New("mysql: context is required")
	errRuntimeInvalidID           = errors.New("mysql: invalid database id")
	errRuntimeDefinitionMismatch  = errors.New("mysql: runtime definition mismatch")
	errRuntimePoolMissing         = errors.New("mysql: runtime pool is missing")
)

type DefinitionPreparer interface {
	Prepare(context.Context, connection.ID) (connection.ResolvedDefinition, error)
}

type Client struct {
	pool     runtimePool
	database string
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
	closeErr    error
	isClosed    bool
}

func NewOpener(preparer DefinitionPreparer) (*Opener, error) {
	return newOpener(openerDependencies{
		preparer:    preparer,
		pools:       newSQLPoolFactory(),
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

func (o *Opener) Open(ctx context.Context, id connection.ID) (*Client, error) {
	client, err := o.openRaw(ctx, id)
	if err != nil {
		return nil, projectMySQLQueryError(ctx, nil, err)
	}

	return client, nil
}

func (o *Opener) OpenQuery(ctx context.Context, id connection.ID) (*Client, error) {
	client, err := o.openRaw(ctx, id)
	if err != nil {
		return nil, projectMySQLQueryError(ctx, nil, err)
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
	if entry := o.entries[id]; entry != nil {
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
		exists && entry != nil && entry.attempt == attempt

	if isCurrent { //nolint:nestif // generation checks and stale-pool disposal must remain atomic.
		if err == nil && client != nil {
			entry.client = client
			entry.attempt = nil
			attempt.client = client
		} else {
			delete(o.entries, id)

			if err == nil {
				err = errRuntimePoolMissing
			}

			attempt.err = err
		}
	} else {
		if client != nil {
			stalePool = client.pool
		}

		switch {
		case o.isClosed:
			attempt.err = ErrRuntimeClosed
		case err != nil:
			attempt.err = err
		default:
			attempt.err = errOpenInvalidated
		}
	}

	close(attempt.done)
	o.mu.Unlock()

	if stalePool != nil {
		o.closeTracked(stalePool)
	}
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

	database := strings.Clone(resolved.Settings[settingDatabase])

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

	if cause := context.Cause(ctx); cause != nil {
		o.closeTracked(pool)
		return nil, cause
	}

	return &Client{pool: pool, database: database}, nil
}

func rawAttemptError(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}

	return err
}

func clearResolvedSecrets(secrets map[string][]byte) {
	for _, value := range secrets {
		clear(value)
	}
}

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

		if err := pool.Close(); err != nil {
			o.mu.Lock()
			o.closeErr = errors.Join(o.closeErr, err)
			o.mu.Unlock()
		}
	}()
}

func (o *Opener) Close(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrShutdownTimeout)
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
		o.startTrackedCloseAlreadyCounted(pool)
	}

	select {
	case <-o.closeDone:
		o.mu.Lock()
		err := o.closeErr
		o.mu.Unlock()

		return err
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrShutdownTimeout, ctx.Err())
	}
}

func (o *Opener) startTrackedCloseAlreadyCounted(pool runtimePool) {
	go func() {
		defer o.closers.Done()

		if err := pool.Close(); err != nil {
			o.mu.Lock()
			o.closeErr = errors.Join(o.closeErr, err)
			o.mu.Unlock()
		}
	}()
}

func (o *Opener) completeClose() {
	o.attempts.Wait()
	o.closers.Wait()
	close(o.closeDone)
}
