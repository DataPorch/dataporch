package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAppCreatesAndRemovesLocalMCPArtifacts(t *testing.T) {
	t.Parallel()

	cfg := initializedTestConfig(t)
	application, err := New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	waitForFile(t, cfg.MCPSocketPath)
	waitForFile(t, cfg.MCPControlTokenPath)
	waitForMode(t, cfg.MCPSocketPath, 0o600)
	waitForMode(t, cfg.MCPControlTokenPath, 0o600)

	for _, path := range []string{cfg.MCPSocketPath, cfg.MCPControlTokenPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode(%q) = %o, want 600", path, got)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, path := range []string{cfg.MCPSocketPath, cfg.MCPControlTokenPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Lstat(%q) error = %v, want not exist", path, err)
		}
	}
}

func TestAppLocalMCPUsesDedicatedCredentialBoundary(t *testing.T) {
	t.Parallel()

	cfg := initializedTestConfig(t)
	application, err := New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	waitForFile(t, cfg.MCPSocketPath)
	waitForFile(t, cfg.MCPControlTokenPath)
	waitForMode(t, cfg.MCPSocketPath, 0o600)
	waitForMode(t, cfg.MCPControlTokenPath, 0o600)
	credential := readLocalCredential(t, cfg.MCPControlTokenPath)

	publicResponse := serveApplicationRequest(t, application, http.MethodGet, "/mcp", "Bearer "+credential)
	if publicResponse.Code != http.StatusUnauthorized || publicResponse.Header().Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
		t.Fatalf("TCP MCP with local credential = %d/%q, want 401/invalid_token", publicResponse.Code, publicResponse.Header().Get("WWW-Authenticate"))
	}

	client := localMCPHTTPClient(t, cfg.MCPSocketPath)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://unix/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	localResponse, err := client.Do(request)
	if err != nil {
		t.Fatalf("local MCP request error = %v", err)
	}
	if localResponse.StatusCode == http.StatusUnauthorized {
		t.Fatalf("local MCP status = 401, want authenticated downstream response")
	}
	_ = localResponse.Body.Close()

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestAppLocalMCPFailureIsFatal(t *testing.T) {
	t.Parallel()

	localErr := errors.New("local MCP failed")
	runtimeErr := errors.New("runtime close failed")
	runtime := &appLifecycleRuntimeTestStub{closeErr: runtimeErr}
	application := &App{
		server:         &http.Server{},
		runtimes:       []runtimeLifecycle{runtime},
		shutdownPeriod: time.Second,
	}
	publicErrors := make(chan error, 1)
	publicErrors <- http.ErrServerClosed
	adminErrors := make(chan error, 1)
	adminErrors <- nil
	localErrors := make(chan error, 1)
	localErrors <- localErr
	ctx, cancel := context.WithCancel(t.Context())

	err := application.waitForServers(ctx, cancel, publicErrors, adminErrors, localErrors)
	if !errors.Is(err, localErr) || !errors.Is(err, runtimeErr) {
		t.Fatalf("waitForServers() error = %v, want local and runtime errors", err)
	}
}

func readLocalCredential(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return strings.TrimSpace(string(data))
}

func localMCPHTTPClient(t *testing.T, path string) *http.Client {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func waitForMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Mode().Perm() == mode {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("path %q did not reach mode %o", path, mode)
}
