package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/connection"
)

func TestNew(t *testing.T) {
	t.Parallel()

	if _, err := New(testConfig(), nil); err == nil {
		t.Fatal("New() error = nil, want logger validation error")
	}
}

func TestApp_Run(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	application, err := New(testConfig(), logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := application.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestNewStartsWithoutInitializedSecretStore(t *testing.T) {
	t.Parallel()

	application, err := New(testConfigFor(t), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if application.manager == nil {
		t.Fatal("manager = nil")
	}
}

func TestAppRunsPublicServerWhenAdminSocketFails(t *testing.T) {
	t.Parallel()

	cfg := testConfigFor(t)
	if err := os.WriteFile(cfg.AdminSocketPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	application, err := New(cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("Run() returned before cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestAppCreatesAndRemovesAdminSocket(t *testing.T) {
	t.Parallel()

	cfg := initializedTestConfig(t)
	application, err := New(cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	waitForFile(t, cfg.AdminSocketPath)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Lstat(cfg.AdminSocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket stat error = %v, want not exist", err)
	}
}

func TestAppLiveImportRegistersWithoutRestart(t *testing.T) {
	t.Parallel()

	cfg := initializedTestConfig(t)
	adapter := &adapterStub{kind: "postgres", parsed: connection.ParsedConnection{
		Settings: map[string]string{"host": "postgres.internal", "database": "finance", "username": "app_reader"},
		Secrets:  map[string][]byte{"password": []byte("dataporch-secret-canary-91f7c2")},
	}}
	application, err := New(cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), adapter)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	}()
	waitForFile(t, cfg.AdminSocketPath)

	response, err := importOverSocket(
		cfg.AdminSocketPath,
		"finance",
		"postgres",
		"postgres://reader:password@host/finance",
	)
	if err != nil {
		t.Fatalf("importOverSocket() error = %v", err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", response.StatusCode)
	}
	_ = response.Body.Close()
	definition, err := application.manager.Lookup("finance")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if definition.Kind != "postgres" || adapter.parseCalls != 1 {
		t.Fatalf("definition/parse calls = %#v/%d", definition, adapter.parseCalls)
	}
}

func TestAppImportDoesNotCallAdapterAuthentication(t *testing.T) {
	t.Parallel()

	cfg := initializedTestConfig(t)
	adapter := &adapterStub{kind: "postgres", parsed: connection.ParsedConnection{
		Settings: map[string]string{"host": "postgres.internal"},
		Secrets:  map[string][]byte{"password": []byte("private")},
	}}
	application, err := New(cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), adapter)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	}()
	waitForFile(t, cfg.AdminSocketPath)
	response, err := importOverSocket(
		cfg.AdminSocketPath,
		"finance",
		"postgres",
		"postgres://reader:password@host/finance",
	)
	if err != nil {
		t.Fatalf("importOverSocket() error = %v", err)
	}
	_ = response.Body.Close()
	if adapter.authenticationCalls != 0 {
		t.Fatalf("authentication calls = %d, want 0", adapter.authenticationCalls)
	}
}

func testConfig() config.Config {
	return config.Config{
		HTTPAddress:          "127.0.0.1:0",
		ResourceLimit:        10,
		ShutdownPeriod:       time.Second,
		AdminSocketPath:      "/tmp/dataporch/admin.sock",
		MasterKeyPath:        "/tmp/dataporch/master.key",
		SecretsStorePath:     "/tmp/dataporch/secrets.store",
		ConnectionsStorePath: "/tmp/dataporch/connections.store",
	}
}

func testConfigFor(t *testing.T) config.Config {
	t.Helper()
	base := t.TempDir()
	return config.Config{
		HTTPAddress:          "127.0.0.1:0",
		ResourceLimit:        10,
		ShutdownPeriod:       time.Second,
		AdminSocketPath:      filepath.Join(base, "admin.sock"),
		MasterKeyPath:        filepath.Join(base, "master.key"),
		SecretsStorePath:     filepath.Join(base, "secrets.store"),
		ConnectionsStorePath: filepath.Join(base, "connections.store"),
	}
}

func initializedTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := testConfigFor(t)
	if err := InitializeSecrets(cfg); err != nil {
		t.Fatalf("InitializeSecrets() error = %v", err)
	}
	return cfg
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%q was not created", path)
}

func importOverSocket(path string, databaseID, kind, connectionString string) (*http.Response, error) {
	dialer := &net.Dialer{Timeout: time.Second}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", path)
			},
		},
	}
	payload, err := json.Marshal(struct {
		DatabaseID       string `json:"databaseId"`
		Kind             string `json:"kind"`
		ConnectionString []byte `json:"connectionString"`
	}{DatabaseID: databaseID, Kind: kind, ConnectionString: []byte(connectionString)})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest(http.MethodPost, "http://unix/v1/connections/import", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	return client.Do(request)
}

type adapterStub struct {
	kind                connection.Kind
	parsed              connection.ParsedConnection
	parseCalls          int
	authenticationCalls int
}

func (a *adapterStub) Kind() connection.Kind { return a.kind }

func (a *adapterStub) ParseConnectionString([]byte) (connection.ParsedConnection, error) {
	a.parseCalls++
	return a.parsed.Clone(), nil
}
