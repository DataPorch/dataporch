package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/DataPorch/dataporch/internal/config"
	"github.com/DataPorch/dataporch/internal/connection"
	"github.com/DataPorch/dataporch/internal/connection/postgres"
	"github.com/DataPorch/dataporch/internal/execution"
)

func TestNewPostgresModule(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)

	module, err := newPostgresModule(manager, validQueryPolicy())
	if err != nil {
		t.Fatalf("newPostgresModule() error = %v", err)
	}

	assertRelationalModule(t, module, postgres.Kind, func(runtime any) bool {
		_, ok := runtime.(*postgres.Opener)
		return ok
	}, "*postgres.Opener")

	if err := module.runtime.Close(t.Context()); err != nil {
		t.Fatalf("runtime.Close() error = %v", err)
	}
}

func TestNewPostgresModuleRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)
	testRelationalModuleRejectsInvalidInputs(t, manager, newPostgresModule)
}

func TestNewConstructsPostgresRuntimeWithoutOpening(t *testing.T) {
	t.Parallel()

	testNewConstructsRelationalRuntimeWithoutOpening(t, newPostgresModule, func(runtime any) bool {
		_, ok := runtime.(*postgres.Opener)
		return ok
	}, "*postgres.Opener")
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
	runtime runtimeLifecycle,
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
	runtime runtimeLifecycle,
	logger *slog.Logger,
) *App {
	t.Helper()

	application, err := newWithDependencies(
		cfg,
		logger,
		appDependencies{
			relationalModuleFactories: []relationalModuleFactory{
				func(*connection.Manager, queryPolicy) (relationalModule, error) {
					return relationalModule{
						adapter:       postgres.New(),
						discoverer:    &relationalDiscovererStub{kind: postgres.Kind},
						queryExecutor: &relationalQueryExecutorStub{kind: postgres.Kind},
						runtime:       runtime,
					}, nil
				},
			},
			newExecutionService: execution.New,
			random:              testRandomReader(),
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
	closeErr            error
	closes              int
	invalidationStarted chan struct{}
	invalidationRelease chan struct{}
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
