package postgres

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/DataPorch/dataporch/internal/connection"
)

func TestNewOpenerValidatesDependencies(t *testing.T) {
	t.Parallel()

	factory := &openerTestFactory{}
	preparer := &openerTestPreparer{}

	tests := []struct {
		name string
		deps openerDependencies
		want error
	}{
		{
			name: "missing preparer",
			deps: openerDependencies{pools: factory, openTimeout: time.Second},
			want: errDefinitionPreparerRequired,
		},
		{
			name: "missing pool factory",
			deps: openerDependencies{preparer: preparer, openTimeout: time.Second},
			want: errPoolFactoryRequired,
		},
		{
			name: "missing timeout",
			deps: openerDependencies{preparer: preparer, pools: factory},
			want: errOpenTimeoutRequired,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := newOpener(test.deps); !errors.Is(err, test.want) {
				t.Fatalf("newOpener() error = %v, want %v", err, test.want)
			}
		})
	}

	if _, err := NewOpener(nil); !errors.Is(err, errDefinitionPreparerRequired) {
		t.Fatalf("NewOpener(nil) error = %v, want %v", err, errDefinitionPreparerRequired)
	}
}

func TestNewOpenerPerformsNoPoolOperation(t *testing.T) {
	t.Parallel()

	opener, err := NewOpener(&openerTestPreparer{definition: resolvedPostgresDefinition()})
	if err != nil {
		t.Fatalf("NewOpener() error = %v", err)
	}

	if opener.openTimeout != initialOpenTimeout {
		t.Errorf("openTimeout = %s, want %s", opener.openTimeout, initialOpenTimeout)
	}

	if len(opener.entries) != 0 {
		t.Fatalf("entries = %#v, want empty", opener.entries)
	}

	if err := opener.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestOpenerOpenCreatesAndPingsPool(t *testing.T) {
	t.Parallel()

	pool := newOpenerTestPool()
	factory := &openerTestFactory{results: []openerPoolResult{{pool: pool}}}
	opener := newTestOpener(t, &openerTestPreparer{definition: resolvedPostgresDefinition()}, factory)

	client, err := opener.Open(t.Context(), "finance")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if client == nil || client.pool != pool {
		t.Fatalf("Open() client = %#v, want client backed by test pool", client)
	}

	if got := pool.pingCount(); got != 1 {
		t.Errorf("Ping() count = %d, want 1", got)
	}

	if got := factory.callCount(); got != 1 {
		t.Errorf("pool factory calls = %d, want 1", got)
	}
}

func TestOpenerOpenReturnsCachedClientWithoutPing(t *testing.T) {
	t.Parallel()

	pool := newOpenerTestPool()
	factory := &openerTestFactory{results: []openerPoolResult{{pool: pool}}}
	opener := newTestOpener(t, &openerTestPreparer{definition: resolvedPostgresDefinition()}, factory)

	first, err := opener.Open(t.Context(), "finance")
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}

	second, err := opener.Open(t.Context(), "finance")
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}

	if first != second {
		t.Errorf("cached clients differ: first %p, second %p", first, second)
	}

	if got := pool.pingCount(); got != 1 {
		t.Errorf("Ping() count = %d, want 1", got)
	}

	if got := factory.callCount(); got != 1 {
		t.Errorf("pool factory calls = %d, want 1", got)
	}
}

func TestOpenerOpenRejectsInvalidRequest(t *testing.T) {
	t.Parallel()

	factory := &openerTestFactory{}
	preparer := &openerTestPreparer{definition: resolvedPostgresDefinition()}
	opener := newTestOpener(t, preparer, factory)

	for _, id := range []connection.ID{"", "not valid", "finance/reporting"} {
		_, err := opener.Open(t.Context(), id)
		if !errors.Is(err, connection.ErrDatabaseUnavailable) {
			t.Errorf("Open(%q) error = %v, want ErrDatabaseUnavailable", id, err)
		}
	}

	if got := factory.callCount(); got != 0 {
		t.Errorf("pool factory calls = %d, want 0", got)
	}

	if got := preparer.callCount(); got != 0 {
		t.Errorf("preparer calls = %d, want 0", got)
	}
}

func TestOpenerOpenClassifiesPreparationFailures(t *testing.T) {
	t.Parallel()

	preparer := &openerTestPreparer{
		definition: resolvedPostgresDefinition(),
		err:        fmt.Errorf("lookup leaked-host: %w", connection.ErrDatabaseNotFound),
	}
	opener := newTestOpener(t, preparer, &openerTestFactory{})

	_, err := opener.Open(t.Context(), "finance")
	if !errors.Is(err, connection.ErrDatabaseUnavailable) {
		t.Errorf("Open() error = %v, want ErrDatabaseUnavailable", err)
	}

	if !errors.Is(err, connection.ErrDatabaseNotFound) {
		t.Errorf("Open() error = %v, want ErrDatabaseNotFound", err)
	}

	if containsAny(err.Error(), "leaked-host") {
		t.Errorf("Open() error = %v, contains raw preparer detail", err)
	}
}

func TestOpenerOpenRejectsMismatchedResolvedID(t *testing.T) {
	t.Parallel()

	definition := resolvedPostgresDefinition()
	definition.ID = "other"
	factory := &openerTestFactory{}
	opener := newTestOpener(t, &openerTestPreparer{definition: definition}, factory)

	_, err := opener.Open(t.Context(), "finance")
	if !errors.Is(err, connection.ErrDatabaseUnavailable) {
		t.Errorf("Open() error = %v, want ErrDatabaseUnavailable", err)
	}

	if got := factory.callCount(); got != 0 {
		t.Errorf("pool factory calls = %d, want 0", got)
	}
}

func TestOpenerOpenRejectsUnsupportedKind(t *testing.T) {
	t.Parallel()

	definition := resolvedPostgresDefinition()
	definition.Kind = "mysql"
	factory := &openerTestFactory{}
	opener := newTestOpener(t, &openerTestPreparer{definition: definition}, factory)

	_, err := opener.Open(t.Context(), "finance")
	if !errors.Is(err, connection.ErrDatabaseUnavailable) {
		t.Errorf("Open() error = %v, want ErrDatabaseUnavailable", err)
	}

	if !errors.Is(err, ErrUnsupportedKind) {
		t.Errorf("Open() error = %v, want ErrUnsupportedKind", err)
	}

	if got := factory.callCount(); got != 0 {
		t.Errorf("pool factory calls = %d, want 0", got)
	}
}

func TestOpenerOpenSanitizesPoolFailures(t *testing.T) {
	t.Parallel()

	creationFactory := &openerTestFactory{
		results: []openerPoolResult{{err: errors.New("fake-driver host=canary-host user=canary-user")}},
	}
	opener := newTestOpener(t, &openerTestPreparer{definition: resolvedPostgresDefinition()}, creationFactory)
	_, err := opener.Open(t.Context(), "finance")
	assertSanitizedOpenError(t, err)

	pingPool := newOpenerTestPool()
	pingPool.pingErr = errors.New("fake-driver password=canary-password database=canary-database")
	pingFactory := &openerTestFactory{results: []openerPoolResult{{pool: pingPool}}}
	pingOpener := newTestOpener(t, &openerTestPreparer{definition: resolvedPostgresDefinition()}, pingFactory)
	_, err = pingOpener.Open(t.Context(), "finance")
	assertSanitizedOpenError(t, err)
	waitForCount(t, pingPool.closeCount, 1)
}

func TestOpenerOpenDoesNotLeakDefinitionFields(t *testing.T) {
	t.Parallel()

	definition := resolvedPostgresDefinition()
	definition.Settings[settingHost] = "canary-host"
	definition.Settings[settingUsername] = "canary-user"
	definition.Settings[settingDatabase] = "canary-database"
	definition.Secrets[settingPassword] = []byte("canary-password")
	factory := &openerTestFactory{
		results: []openerPoolResult{{err: errors.New("fake-driver failure")}},
	}
	opener := newTestOpener(t, &openerTestPreparer{definition: definition}, factory)

	_, err := opener.Open(t.Context(), "finance")
	if err == nil {
		t.Fatal("Open() error = nil, want failure")
	}

	if containsAny(err.Error(), "canary-host", "canary-user", "canary-database", "canary-password", "fake-driver") {
		t.Errorf("Open() error = %v, contains definition or driver detail", err)
	}
}

func TestOpenerOpenClearsResolvedSecretBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		factory   *openerTestFactory
		wantError bool
	}{
		{
			name:    "success",
			factory: &openerTestFactory{results: []openerPoolResult{{pool: newOpenerTestPool()}}},
		},
		{
			name: "failure",
			factory: &openerTestFactory{
				results: []openerPoolResult{{err: errors.New("fake-driver failure")}},
			},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			definition := resolvedPostgresDefinition()
			preparer := &openerTestPreparer{
				definition: definition,
				clone:      false,
				retain:     true,
			}
			opener := newTestOpener(t, preparer, test.factory)

			_, err := opener.Open(t.Context(), "finance")
			if (err != nil) != test.wantError {
				t.Fatalf("Open() error = %v, want error %t", err, test.wantError)
			}

			for _, value := range preparer.retainedBytes() {
				for index, byteValue := range value {
					if byteValue != 0 {
						t.Fatalf("retained secret byte %d = %d, want zero", index, byteValue)
					}
				}
			}
		})
	}
}

func TestOpenerOpenCoalescesSameID(t *testing.T) {
	t.Parallel()

	pool := newOpenerTestPool()
	pool.pingRelease = make(chan struct{})
	pool.pingWait = true
	factory := &openerTestFactory{results: []openerPoolResult{{pool: pool}}}
	preparer := &openerTestPreparer{definition: resolvedPostgresDefinition()}
	opener := newTestOpener(t, preparer, factory)

	firstResult := make(chan openerResult, 1)

	go func() {
		client, err := opener.Open(context.Background(), "finance")
		firstResult <- openerResult{client: client, err: err}
	}()

	waitForSignal(t, pool.pingStarted)

	secondResult := make(chan openerResult, 1)

	go func() {
		client, err := opener.Open(context.Background(), "finance")
		secondResult <- openerResult{client: client, err: err}
	}()

	if got := factory.callCount(); got != 1 {
		t.Errorf("pool factory calls = %d, want 1", got)
	}

	close(pool.pingRelease)

	first := receiveResult(t, firstResult)

	second := receiveResult(t, secondResult)
	if first.err != nil || second.err != nil {
		t.Fatalf("Open() errors = %v, %v", first.err, second.err)
	}

	if first.client != second.client {
		t.Errorf("coalesced clients differ: %p, %p", first.client, second.client)
	}
}

func TestOpenerOpenAllowsDifferentIDsConcurrently(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	started := make(chan struct{}, 2)
	preparer := &openerTestPreparer{
		definition: resolvedPostgresDefinition(),
		autoID:     true,
	}
	factory := &openerTestFactory{}
	factory.newFn = func(_ context.Context, _ connection.ResolvedDefinition) (runtimePool, error) {
		pool := newOpenerTestPool()
		pool.pingSignal = started
		pool.pingRelease = release
		pool.pingWait = true

		return pool, nil
	}
	opener := newTestOpener(t, preparer, factory)

	results := make(chan openerResult, 2)

	for _, id := range []connection.ID{"finance", "reporting"} {
		go func(id connection.ID) {
			client, err := opener.Open(context.Background(), id)
			results <- openerResult{client: client, err: err}
		}(id)
	}

	waitForSignal(t, started)
	waitForSignal(t, started)

	if got := factory.callCount(); got != 2 {
		t.Errorf("pool factory calls = %d, want 2", got)
	}

	close(release)

	for range 2 {
		result := receiveResult(t, results)
		if result.err != nil || result.client == nil {
			t.Errorf("Open() result = %#v, want success", result)
		}
	}
}

func TestOpenerOpenLetsWaiterCancelIndependently(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		pool := newOpenerTestPool()
		pool.pingRelease = make(chan struct{})
		pool.pingWait = true
		factory := &openerTestFactory{results: []openerPoolResult{{pool: pool}}}
		opener := newTestOpenerWithTimeout(
			t,
			&openerTestPreparer{definition: resolvedPostgresDefinition()},
			factory,
			10*time.Second,
		)

		firstResult := make(chan openerResult, 1)

		go func() {
			client, err := opener.Open(context.Background(), "finance")
			firstResult <- openerResult{client: client, err: err}
		}()

		synctest.Wait()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err := opener.Open(ctx, "finance")
		if !errors.Is(err, context.DeadlineExceeded) ||
			!errors.Is(err, connection.ErrDatabaseUnavailable) {
			t.Fatalf("canceled waiter error = %v, want deadline and ErrDatabaseUnavailable", err)
		}

		if got := factory.callCount(); got != 1 {
			t.Errorf("pool factory calls = %d, want 1", got)
		}

		close(pool.pingRelease)

		first := <-firstResult
		if first.err != nil || first.client == nil {
			t.Fatalf("uncanceled waiter result = %#v, want success", first)
		}
	})
}

func TestOpenerOpenTimesOutSharedAttempt(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		pool := newOpenerTestPool()
		pool.pingRelease = make(chan struct{})
		pool.pingWait = true
		factory := &openerTestFactory{results: []openerPoolResult{{pool: pool}}}
		preparer := &openerTestPreparer{definition: resolvedPostgresDefinition()}
		opener := newTestOpenerWithTimeout(t, preparer, factory, 5*time.Second)

		_, err := opener.Open(context.Background(), "finance")
		if !errors.Is(err, ErrOpenTimeout) {
			t.Errorf("Open() error = %v, want ErrOpenTimeout", err)
		}

		if !errors.Is(err, connection.ErrDatabaseUnavailable) {
			t.Errorf("Open() error = %v, want ErrDatabaseUnavailable", err)
		}
	})
}

func TestOpenerOpenDoesNotCacheFailure(t *testing.T) {
	t.Parallel()

	pool := newOpenerTestPool()
	factory := &openerTestFactory{
		results: []openerPoolResult{
			{err: errors.New("fake-driver first failure")},
			{pool: pool},
		},
	}
	opener := newTestOpener(t, &openerTestPreparer{definition: resolvedPostgresDefinition()}, factory)

	if _, err := opener.Open(t.Context(), "finance"); err == nil {
		t.Fatal("first Open() error = nil, want failure")
	}

	client, err := opener.Open(t.Context(), "finance")
	if err != nil || client == nil {
		t.Fatalf("second Open() = %#v, %v, want success", client, err)
	}

	if got := factory.callCount(); got != 2 {
		t.Errorf("pool factory calls = %d, want 2", got)
	}
}

func TestOpenerInvalidateClosesCachedPoolAsynchronously(t *testing.T) {
	t.Parallel()

	pool := newOpenerTestPool()
	pool.closeStarted = make(chan struct{})
	pool.closeRelease = make(chan struct{})
	pool.closeWait = true
	factory := &openerTestFactory{results: []openerPoolResult{{pool: pool}}}

	opener := newTestOpener(t, &openerTestPreparer{definition: resolvedPostgresDefinition()}, factory)
	if _, err := opener.Open(t.Context(), "finance"); err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	start := time.Now()

	opener.Invalidate("finance")

	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Invalidate() took %s, want asynchronous close", elapsed)
	}

	waitForSignal(t, pool.closeStarted)

	if got := pool.closeCount(); got != 1 {
		t.Errorf("Close() count = %d, want 1", got)
	}

	close(pool.closeRelease)
	waitForCount(t, pool.closeCount, 1)
}

func TestOpenerInvalidatePreventsStalePublish(t *testing.T) {
	t.Parallel()

	firstPool := newOpenerTestPool()
	firstPool.pingStarted = make(chan struct{})
	firstPool.pingRelease = make(chan struct{})
	firstPool.pingWait = true
	firstPool.pingIgnoreContext = true
	secondPool := newOpenerTestPool()
	factory := &openerTestFactory{
		results: []openerPoolResult{{pool: firstPool}, {pool: secondPool}},
	}
	opener := newTestOpener(t, &openerTestPreparer{definition: resolvedPostgresDefinition()}, factory)

	result := make(chan openerResult, 1)

	go func() {
		client, err := opener.Open(context.Background(), "finance")
		result <- openerResult{client: client, err: err}
	}()

	waitForSignal(t, firstPool.pingStarted)
	opener.Invalidate("finance")
	close(firstPool.pingRelease)

	first := receiveResult(t, result)
	if first.client != nil || !errors.Is(first.err, connection.ErrDatabaseUnavailable) {
		t.Fatalf("stale Open() result = %#v, want unavailable error", first)
	}

	if _, err := opener.Open(t.Context(), "finance"); err != nil {
		t.Fatalf("replacement Open() error = %v", err)
	}

	if got := factory.callCount(); got != 2 {
		t.Errorf("pool factory calls = %d, want 2", got)
	}

	waitForCount(t, firstPool.closeCount, 1)
}

func TestOpenerCloseCancelsAttemptsAndClosesPools(t *testing.T) {
	t.Parallel()

	readyPool := newOpenerTestPool()
	attemptPool := newOpenerTestPool()
	attemptPool.pingStarted = make(chan struct{})
	attemptPool.pingRelease = make(chan struct{})
	attemptPool.pingWait = true
	preparer := &openerTestPreparer{
		definition: resolvedPostgresDefinition(),
		autoID:     true,
	}
	factory := &openerTestFactory{results: []openerPoolResult{{pool: readyPool}, {pool: attemptPool}}}

	opener := newTestOpener(t, preparer, factory)
	if _, err := opener.Open(t.Context(), "finance"); err != nil {
		t.Fatalf("ready Open() error = %v", err)
	}

	result := make(chan openerResult, 1)

	go func() {
		client, err := opener.Open(context.Background(), "reporting")
		result <- openerResult{client: client, err: err}
	}()

	waitForSignal(t, attemptPool.pingStarted)

	if err := opener.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	attempt := receiveResult(t, result)
	if attempt.client != nil || !errors.Is(attempt.err, ErrRuntimeClosed) {
		t.Fatalf("opening result = %#v, want runtime closed", attempt)
	}

	waitForCount(t, readyPool.closeCount, 1)
	waitForCount(t, attemptPool.closeCount, 1)
}

func TestOpenerCloseIsIdempotentAndBounded(t *testing.T) {
	t.Parallel()

	pool := newOpenerTestPool()
	pool.closeStarted = make(chan struct{})
	pool.closeRelease = make(chan struct{})
	pool.closeWait = true
	factory := &openerTestFactory{results: []openerPoolResult{{pool: pool}}}

	opener := newTestOpener(t, &openerTestPreparer{definition: resolvedPostgresDefinition()}, factory)
	if _, err := opener.Open(t.Context(), "finance"); err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := opener.Close(ctx)
	if !errors.Is(err, connection.ErrDatabaseUnavailable) ||
		!errors.Is(err, ErrShutdownTimeout) ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded Close() error = %v, want shutdown timeout classifications", err)
	}

	waitForSignal(t, pool.closeStarted)
	close(pool.closeRelease)
	waitForCount(t, pool.closeCount, 1)

	if err := opener.Close(context.Background()); err != nil {
		t.Fatalf("completion Close() error = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if err := opener.Close(canceled); err != nil {
		t.Fatalf("completed repeated Close() error = %v, want nil", err)
	}
}

func TestOpenerOpenAfterClose(t *testing.T) {
	t.Parallel()

	opener := newTestOpener(t, &openerTestPreparer{definition: resolvedPostgresDefinition()}, &openerTestFactory{})
	if err := opener.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err := opener.Open(t.Context(), "finance")
	if !errors.Is(err, ErrRuntimeClosed) {
		t.Errorf("Open() after Close() error = %v, want ErrRuntimeClosed", err)
	}
}

func TestOpenerConcurrentOpenInvalidateAndClose(t *testing.T) {
	t.Parallel()

	preparer := &openerTestPreparer{
		definition: resolvedPostgresDefinition(),
		autoID:     true,
	}
	factory := &openerTestFactory{}
	factory.newFn = func(_ context.Context, _ connection.ResolvedDefinition) (runtimePool, error) {
		return newOpenerTestPool(), nil
	}
	opener := newTestOpener(t, preparer, factory)

	start := make(chan struct{})

	var workers sync.WaitGroup
	for index := range 12 {
		workers.Go(func() {
			<-start

			for range 20 {
				id := connection.ID("finance")
				if index%2 == 0 {
					id = "reporting"
				}

				_, _ = opener.Open(context.Background(), id)
			}
		})
	}

	workers.Go(func() {
		<-start

		for range 30 {
			opener.Invalidate("finance")
			opener.Invalidate("reporting")
		}
	})

	workers.Go(func() {
		<-start

		_ = opener.Close(context.Background())
	})

	close(start)
	workers.Wait()

	if err := opener.Close(context.Background()); err != nil {
		t.Fatalf("final Close() error = %v", err)
	}
}

type openerResult struct {
	client *Client
	err    error
}

type openerPoolResult struct {
	pool runtimePool
	err  error
}

type openerTestPreparer struct {
	mu         sync.Mutex
	definition connection.ResolvedDefinition
	err        error
	autoID     bool
	clone      bool
	retain     bool
	calls      int
	retained   [][]byte
}

func (p *openerTestPreparer) Prepare(_ context.Context, id connection.ID) (connection.ResolvedDefinition, error) {
	p.mu.Lock()
	p.calls++
	definition := p.definition
	prepareErr := p.err
	autoID := p.autoID
	clone := p.clone
	retain := p.retain
	p.mu.Unlock()

	if prepareErr != nil {
		return connection.ResolvedDefinition{}, prepareErr
	}

	if autoID {
		definition.ID = id
	}

	if retain {
		p.mu.Lock()
		for _, value := range definition.Secrets {
			p.retained = append(p.retained, value)
		}
		p.mu.Unlock()
	}

	if clone {
		return cloneResolvedDefinition(definition), nil
	}

	return definition, nil
}

func (p *openerTestPreparer) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.calls
}

func (p *openerTestPreparer) retainedBytes() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([][]byte(nil), p.retained...)
}

type openerTestFactory struct {
	mu      sync.Mutex
	results []openerPoolResult
	newFn   func(context.Context, connection.ResolvedDefinition) (runtimePool, error)
	calls   int
}

func (f *openerTestFactory) New(ctx context.Context, definition connection.ResolvedDefinition) (runtimePool, error) {
	f.mu.Lock()
	index := f.calls
	f.calls++
	newFn := f.newFn

	var result openerPoolResult
	if index < len(f.results) {
		result = f.results[index]
	}
	f.mu.Unlock()

	if newFn != nil {
		return newFn(ctx, definition)
	}

	if result.err != nil {
		return nil, result.err
	}

	if result.pool == nil {
		return newOpenerTestPool(), nil
	}

	return result.pool, nil
}

func (f *openerTestFactory) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

type openerTestPool struct {
	mu                sync.Mutex
	pingCalls         int
	closeCalls        int
	pingErr           error
	pingStarted       chan struct{}
	pingSignal        chan struct{}
	pingRelease       chan struct{}
	pingWait          bool
	pingIgnoreContext bool
	closeStarted      chan struct{}
	closeRelease      chan struct{}
	closeWait         bool
	pingStartedOnce   sync.Once
	closeStartedOnce  sync.Once
}

func (p *openerTestPool) Query(context.Context, string, ...any) (catalogRows, error) {
	return nil, errors.New("opener test pool query not configured")
}

func newOpenerTestPool() *openerTestPool {
	return &openerTestPool{pingStarted: make(chan struct{})}
}

func (p *openerTestPool) Ping(ctx context.Context) error {
	p.mu.Lock()
	p.pingCalls++
	started := p.pingStarted
	release := p.pingRelease
	wait := p.pingWait
	ignoreContext := p.pingIgnoreContext
	pingErr := p.pingErr
	p.mu.Unlock()

	if started != nil {
		p.pingStartedOnce.Do(func() { close(started) })
	}

	if p.pingSignal != nil {
		p.pingSignal <- struct{}{}
	}

	if !wait || release == nil {
		return pingErr
	}

	if ignoreContext {
		<-release
		return pingErr
	}

	select {
	case <-release:
		return pingErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *openerTestPool) Close() {
	p.mu.Lock()
	p.closeCalls++
	started := p.closeStarted
	release := p.closeRelease
	wait := p.closeWait
	p.mu.Unlock()

	if started != nil {
		p.closeStartedOnce.Do(func() { close(started) })
	}

	if wait && release != nil {
		<-release
	}
}

func (p *openerTestPool) pingCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.pingCalls
}

func (p *openerTestPool) closeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.closeCalls
}

func newTestOpener(t *testing.T, preparer *openerTestPreparer, factory *openerTestFactory) *Opener {
	t.Helper()
	return newTestOpenerWithTimeout(t, preparer, factory, time.Second)
}

func newTestOpenerWithTimeout(
	t *testing.T,
	preparer *openerTestPreparer,
	factory *openerTestFactory,
	timeout time.Duration,
) *Opener {
	t.Helper()

	if preparer.clone == false && !preparer.retain {
		preparer.clone = true
	}

	opener, err := newOpener(openerDependencies{
		preparer:    preparer,
		pools:       factory,
		openTimeout: timeout,
	})
	if err != nil {
		t.Fatalf("newOpener() error = %v", err)
	}

	t.Cleanup(func() {
		if err := opener.Close(context.Background()); err != nil {
			t.Errorf("cleanup Close() error = %v", err)
		}
	})

	return opener
}

func cloneResolvedDefinition(definition connection.ResolvedDefinition) connection.ResolvedDefinition {
	settings := maps.Clone(definition.Settings)

	secrets := make(map[string][]byte, len(definition.Secrets))
	for name, value := range definition.Secrets {
		secrets[name] = append([]byte(nil), value...)
	}

	return connection.ResolvedDefinition{
		ID:       definition.ID,
		Kind:     definition.Kind,
		Settings: settings,
		Secrets:  secrets,
	}
}

func assertSanitizedOpenError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("Open() error = nil, want failure")
	}

	if !errors.Is(err, connection.ErrDatabaseUnavailable) {
		t.Errorf("Open() error = %v, want ErrDatabaseUnavailable", err)
	}

	if containsAny(err.Error(), "fake-driver", "canary-host", "canary-user", "canary-database", "canary-password") {
		t.Errorf("Open() error = %v, contains unsanitized detail", err)
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}

	return false
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func receiveResult(t *testing.T, results <-chan openerResult) openerResult {
	t.Helper()

	select {
	case result := <-results:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for opener result")
		return openerResult{}
	}
}

func waitForCount(t *testing.T, count func() int, want int) {
	t.Helper()

	timer := time.NewTimer(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)

	defer timer.Stop()
	defer ticker.Stop()

	for {
		if got := count(); got >= want {
			return
		}

		select {
		case <-timer.C:
			t.Fatalf("timed out waiting for count %d, got %d", want, count())
		case <-ticker.C:
		}
	}
}
