package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
)

func TestAppRunClosesRuntimeWhenContextIsAlreadyCanceled(t *testing.T) {
	t.Parallel()

	runtime := &appLifecycleRuntimeTestStub{}
	application := newAppWithPostgresRuntimeTestStub(t, testConfigFor(t), runtime)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := application.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if runtime.closeCalls() != 1 {
		t.Fatalf("runtime close calls = %d, want 1", runtime.closeCalls())
	}

	if !runtime.closeHasDeadline() {
		t.Fatal("runtime close context has no deadline")
	}

	if runtime.closeContextError() != nil {
		t.Fatalf("runtime close context error = %v, want nil", runtime.closeContextError())
	}
}

func TestAppShutdownClosesPostgresRuntimeAfterTransports(t *testing.T) {
	t.Parallel()

	cfg := initializedTestConfig(t)
	runtime := &appLifecycleRuntimeTestStub{
		onClose: func() {
			if _, err := os.Lstat(cfg.AdminSocketPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("admin socket at runtime close = %v, want not exist", err)
			}
		},
	}
	application := newAppWithPostgresRuntimeTestStub(t, cfg, runtime)

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	waitForFile(t, cfg.AdminSocketPath)
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if runtime.closeCalls() != 1 {
		t.Fatalf("runtime close calls = %d, want 1", runtime.closeCalls())
	}
}

func TestAppShutdownJoinsPostgresRuntimeError(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("runtime close failed")
	runtime := &appLifecycleRuntimeTestStub{closeErr: closeErr}
	cfg := initializedTestConfig(t)
	application := newAppWithPostgresRuntimeTestStub(t, cfg, runtime)

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	waitForFile(t, cfg.AdminSocketPath)
	cancel()

	err := <-done
	if !errors.Is(err, closeErr) {
		t.Fatalf("Run() error = %v, want %v", err, closeErr)
	}

	if runtime.closeCalls() != 1 {
		t.Fatalf("runtime close calls = %d, want 1", runtime.closeCalls())
	}
}

func TestAppShutdownDoesNotLogStoppedAfterRuntimeError(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("runtime close failed")
	cfg := initializedTestConfig(t)
	runtime := &appLifecycleRuntimeTestStub{closeErr: closeErr}

	var logs bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logs, nil))
	application := newAppWithPostgresRuntimeTestStubAndLogger(t, cfg, runtime, logger)

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	waitForFile(t, cfg.AdminSocketPath)
	cancel()

	if err := <-done; !errors.Is(err, closeErr) {
		t.Fatalf("Run() error = %v, want %v", err, closeErr)
	}

	if bytes.Contains(logs.Bytes(), []byte("dataporch stopped")) {
		t.Fatalf("logs contain stopped message after runtime error: %s", logs.Bytes())
	}
}

func TestAppUnexpectedPublicExitClosesRuntime(t *testing.T) {
	t.Parallel()

	cfg := initializedTestConfig(t)
	runtime := &appLifecycleRuntimeTestStub{}
	application := newAppWithPostgresRuntimeTestStub(t, cfg, runtime)

	ctx := t.Context()

	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	waitForFile(t, cfg.AdminSocketPath)

	if err := application.server.Close(); err != nil {
		t.Fatalf("Server.Close() error = %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if runtime.closeCalls() != 1 {
		t.Fatalf("runtime close calls = %d, want 1", runtime.closeCalls())
	}
}

func TestAppShutdownBoundsRuntimeClose(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("runtime close timed out")
	runtime := &appLifecycleRuntimeTestStub{
		blockUntilContextDone: true,
		closeErr:              closeErr,
		closeStarted:          make(chan struct{}),
	}
	cfg := initializedTestConfig(t)
	cfg.ShutdownPeriod = 25 * time.Millisecond
	application := newAppWithPostgresRuntimeTestStub(t, cfg, runtime)

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	waitForFile(t, cfg.AdminSocketPath)

	started := time.Now()

	cancel()

	select {
	case <-runtime.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("runtime close did not start")
	}

	err := <-done
	if !errors.Is(err, closeErr) {
		t.Fatalf("Run() error = %v, want %v", err, closeErr)
	}

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shutdown elapsed time = %v, want less than 1s", elapsed)
	}
}

func TestWaitForServersJoinsUnexpectedServeAndRuntimeErrors(t *testing.T) {
	t.Parallel()

	serveErr := errors.New("public serve failed")
	closeErr := errors.New("runtime close failed")
	runtime := &appLifecycleRuntimeTestStub{closeErr: closeErr}
	application := &App{
		server:         &http.Server{},
		runtimes:       []runtimeLifecycle{runtime},
		shutdownPeriod: time.Second,
	}

	ctx := t.Context()

	publicErrors := make(chan error, 1)
	publicErrors <- serveErr

	adminErrors := make(chan error, 1)
	adminErrors <- nil

	runCtx, cancel := context.WithCancel(ctx)

	err := application.waitForServers(runCtx, cancel, publicErrors, adminErrors, nil)
	if !errors.Is(err, serveErr) {
		t.Fatalf("waitForServers() error = %v, want serving error", err)
	}

	if !errors.Is(err, closeErr) {
		t.Fatalf("waitForServers() error = %v, want runtime error", err)
	}
}

func TestAppCloseRuntimesClosesEveryRuntimeAndPreservesErrorOrder(t *testing.T) {
	t.Parallel()

	firstErr := errors.New("first runtime failed")
	secondErr := errors.New("second runtime failed")
	first := &relationalRuntimeStub{name: "first", closeErr: firstErr}
	second := &relationalRuntimeStub{name: "second", closeErr: secondErr}
	third := &relationalRuntimeStub{name: "third"}
	application := &App{runtimes: []runtimeLifecycle{
		first,
		second,
		third,
	}}

	err := application.closeRuntimes(t.Context())
	if got, want := err.Error(), "first runtime failed\nsecond runtime failed"; got != want {
		t.Fatalf("close error = %q, want %q", got, want)
	}

	for _, runtime := range []*relationalRuntimeStub{first, second, third} {
		if got := relationalRuntimeCloseCalls(runtime); got != 1 {
			t.Fatalf("%s runtime close calls = %d, want 1", runtime.name, got)
		}
	}

	for _, expected := range []error{firstErr, secondErr} {
		if !errors.Is(err, expected) {
			t.Errorf("close error = %v, want %v", err, expected)
		}
	}
}

func TestAppCloseRuntimesWithNoRuntimesIsSafe(t *testing.T) {
	t.Parallel()

	application := &App{runtimes: []runtimeLifecycle{}}
	if err := application.closeRuntimes(t.Context()); err != nil {
		t.Fatalf("closeRuntimes() error = %v, want nil", err)
	}
}

func TestAppRunCanCloseRuntimesRepeatedly(t *testing.T) {
	t.Parallel()

	runtime := &relationalRuntimeStub{name: "alpha"}
	application := &App{
		runtimes:       []runtimeLifecycle{runtime},
		shutdownPeriod: time.Second,
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for range 2 {
		if err := application.Run(ctx); err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
	}

	if got := relationalRuntimeCloseCalls(runtime); got != 2 {
		t.Fatalf("runtime close calls = %d, want 2", got)
	}
}

type appLifecycleRuntimeTestStub struct {
	mu                    sync.Mutex
	closeErr              error
	calls                 int
	hasDeadline           bool
	closeContextErr       error
	blockUntilContextDone bool
	closeStarted          chan struct{}
	closeStartedOnce      sync.Once
	onClose               func()
}

func (r *appLifecycleRuntimeTestStub) Invalidate(connection.ID) {}

func (r *appLifecycleRuntimeTestStub) Close(ctx context.Context) error {
	_, hasDeadline := ctx.Deadline()

	r.mu.Lock()
	r.calls++
	r.hasDeadline = hasDeadline
	r.closeContextErr = ctx.Err()
	block := r.blockUntilContextDone
	closeErr := r.closeErr
	onClose := r.onClose
	closeStarted := r.closeStarted
	r.mu.Unlock()

	if closeStarted != nil {
		r.closeStartedOnce.Do(func() { close(closeStarted) })
	}

	if block {
		<-ctx.Done()
	}

	if onClose != nil {
		onClose()
	}

	return closeErr
}

func (r *appLifecycleRuntimeTestStub) closeCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.calls
}

func (r *appLifecycleRuntimeTestStub) closeHasDeadline() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.hasDeadline
}

func (r *appLifecycleRuntimeTestStub) closeContextError() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.closeContextErr
}
