package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/connection/postgres"
)

func TestNewPostgresModule(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)
	module, err := newPostgresModule(manager, validQueryPolicy())
	if err != nil {
		t.Fatalf("newPostgresModule() error = %v", err)
	}

	if module.adapter.Kind() != postgres.Kind {
		t.Fatalf("adapter kind = %q, want %q", module.adapter.Kind(), postgres.Kind)
	}
	if module.discoverer.Kind() != postgres.Kind || module.queryExecutor.Kind() != postgres.Kind {
		t.Fatal("PostgreSQL execution components disagree with adapter kind")
	}
	if _, ok := module.runtime.(*postgres.Opener); !ok {
		t.Fatalf("runtime type = %T, want *postgres.Opener", module.runtime)
	}

	if err := module.runtime.Close(t.Context()); err != nil {
		t.Fatalf("runtime.Close() error = %v", err)
	}
}

func TestNewPostgresModuleRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)
	tests := []struct {
		name    string
		manager *connection.Manager
		policy  queryPolicy
	}{
		{name: "nil manager", manager: nil, policy: validQueryPolicy()},
		{name: "missing timeout", manager: manager, policy: queryPolicy{
			responseByteLimit: 1024,
			truncationEnabled: true,
			rowLimit:          100,
		}},
		{name: "missing response byte limit", manager: manager, policy: queryPolicy{
			timeout:           time.Second,
			truncationEnabled: true,
			rowLimit:          100,
		}},
		{name: "missing truncation row limit", manager: manager, policy: queryPolicy{
			timeout:           time.Second,
			responseByteLimit: 1024,
			truncationEnabled: true,
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			module, err := newPostgresModule(test.manager, test.policy)
			if err == nil {
				t.Fatal("newPostgresModule() error = nil, want error")
			}
			if !reflect.DeepEqual(module, relationalModule{}) {
				t.Fatalf("newPostgresModule() module = %#v, want zero module", module)
			}
		})
	}
}

func TestNewConstructsPostgresRuntimeWithoutOpening(t *testing.T) {
	t.Parallel()

	runtime := &appPostgresRuntimeTestStub{}

	var preparer postgres.DefinitionPreparer

	constructions := 0

	application, err := newWithDependencies(testConfigFor(t), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), appDependencies{
		adapters: []connection.Adapter{postgres.New()},
		newPostgresRuntime: func(gotPreparer postgres.DefinitionPreparer) (postgresRuntime, error) {
			constructions++
			preparer = gotPreparer

			return runtime, nil
		},
	})
	if err != nil {
		t.Fatalf("newWithDependencies() error = %v", err)
	}

	if application.postgresRuntime != runtime {
		t.Fatal("application did not retain the constructed Postgres runtime")
	}

	if constructions != 1 {
		t.Fatalf("runtime constructions = %d, want 1", constructions)
	}

	if preparer == nil {
		t.Fatal("runtime preparer = nil")
	}

	if got := runtime.invalidatedIDs(); len(got) != 0 {
		t.Fatalf("invalidations during construction = %v, want none", got)
	}

	if runtime.queryOpenCalls() != 0 {
		t.Fatalf("query opens during construction = %d, want 0", runtime.queryOpenCalls())
	}

	if runtime.closeCalls() != 0 {
		t.Fatalf("runtime closes during construction = %d, want 0", runtime.closeCalls())
	}
}

func TestAppSuccessfulImportInvalidatesPostgresRuntime(t *testing.T) {
	t.Parallel()

	cfg := initializedTestConfig(t)
	runtime := &appPostgresRuntimeTestStub{}
	application := newAppWithPostgresRuntimeTestStub(t, cfg, runtime)

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})

	waitForFile(t, cfg.AdminSocketPath)

	response, err := importOverSocket(
		cfg.AdminSocketPath,
		"finance",
		string(postgres.Kind),
		"postgresql://reader:password@postgres-import-test.invalid/finance",
	)
	if err != nil {
		t.Fatalf("importOverSocket() error = %v", err)
	}

	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusCreated)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	runtime.blockNextInvalidation(started, release)

	var releaseOnce sync.Once

	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	secondResponse := make(chan struct {
		status int
		err    error
	}, 1)
	go func() {
		status, err := importStatusOverSocket(
			cfg.AdminSocketPath,
			"finance",
			string(postgres.Kind),
			"postgresql://reader:password@postgres-import-test.invalid/finance",
		)
		secondResponse <- struct {
			status int
			err    error
		}{status: status, err: err}
	}()

	select {
	case <-started:
	case result := <-secondResponse:
		t.Fatalf("replacement response arrived before invalidation: err = %v", result.err)
	case <-time.After(time.Second):
		t.Fatal("replacement invalidation did not start")
	}

	releaseOnce.Do(func() { close(release) })

	result := <-secondResponse
	if result.err != nil {
		t.Fatalf("replacement import error = %v", result.err)
	}

	if result.status != http.StatusOK {
		t.Fatalf("replacement status = %d, want %d", result.status, http.StatusOK)
	}

	invalidated := runtime.invalidatedIDs()
	if len(invalidated) != 2 || invalidated[0] != "finance" || invalidated[1] != "finance" {
		t.Fatalf("invalidated IDs = %v, want [finance finance]", invalidated)
	}
}

func TestAppFailedImportPreservesPostgresRuntime(t *testing.T) {
	t.Parallel()

	cfg := initializedTestConfig(t)
	runtime := &appPostgresRuntimeTestStub{}
	application := newAppWithPostgresRuntimeTestStub(t, cfg, runtime)

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})

	waitForFile(t, cfg.AdminSocketPath)

	response, err := importOverSocket(
		cfg.AdminSocketPath,
		"finance",
		string(postgres.Kind),
		"postgresql://reader@postgres-import-test.invalid/finance",
	)
	if err != nil {
		t.Fatalf("importOverSocket() error = %v", err)
	}

	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	if response.StatusCode == http.StatusCreated {
		t.Fatal("failed import returned created status")
	}

	if invalidated := runtime.invalidatedIDs(); len(invalidated) != 0 {
		t.Fatalf("invalidated IDs = %v, want none", invalidated)
	}
}

func newAppWithPostgresRuntimeTestStub(
	t *testing.T,
	cfg config.Config,
	runtime postgresRuntime,
) *App {
	t.Helper()

	return newAppWithPostgresRuntimeTestStubAndLogger(
		t,
		cfg,
		runtime,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	)
}

func newAppWithPostgresRuntimeTestStubAndLogger(
	t *testing.T,
	cfg config.Config,
	runtime postgresRuntime,
	logger *slog.Logger,
) *App {
	t.Helper()

	application, err := newWithDependencies(
		cfg,
		logger,
		appDependencies{
			adapters: []connection.Adapter{postgres.New()},
			newPostgresRuntime: func(postgres.DefinitionPreparer) (postgresRuntime, error) {
				return runtime, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("newWithDependencies() error = %v", err)
	}

	return application
}

func importStatusOverSocket(path, databaseID, kind, connectionString string) (int, error) {
	response, err := importOverSocket(path, databaseID, kind, connectionString)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()

	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		return 0, err
	}

	return response.StatusCode, nil
}

type appPostgresRuntimeTestStub struct {
	mu                  sync.Mutex
	invalidated         []connection.ID
	queryOpens          []connection.ID
	closeErr            error
	closes              int
	invalidationStarted chan struct{}
	invalidationRelease chan struct{}
}

func (r *appPostgresRuntimeTestStub) Open(context.Context, connection.ID) (*postgres.Client, error) {
	return nil, nil
}

func (r *appPostgresRuntimeTestStub) OpenQuery(
	ctx context.Context,
	id connection.ID,
) (*postgres.Client, error) {
	r.mu.Lock()
	r.queryOpens = append(r.queryOpens, id)
	r.mu.Unlock()

	return r.Open(ctx, id)
}

func (r *appPostgresRuntimeTestStub) Invalidate(id connection.ID) {
	r.mu.Lock()
	r.invalidated = append(r.invalidated, id)
	started := r.invalidationStarted
	release := r.invalidationRelease
	r.invalidationStarted = nil
	r.invalidationRelease = nil
	r.mu.Unlock()

	if started != nil {
		close(started)
	}

	if release != nil {
		<-release
	}
}

func (r *appPostgresRuntimeTestStub) blockNextInvalidation(
	started chan struct{},
	release chan struct{},
) {
	r.mu.Lock()
	r.invalidationStarted = started
	r.invalidationRelease = release
	r.mu.Unlock()
}

func (r *appPostgresRuntimeTestStub) Close(context.Context) error {
	r.mu.Lock()
	r.closes++
	err := r.closeErr
	r.mu.Unlock()

	return err
}

func (r *appPostgresRuntimeTestStub) invalidatedIDs() []connection.ID {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]connection.ID(nil), r.invalidated...)
}

func (r *appPostgresRuntimeTestStub) closeCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.closes
}

func (r *appPostgresRuntimeTestStub) queryOpenCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.queryOpens)
}
