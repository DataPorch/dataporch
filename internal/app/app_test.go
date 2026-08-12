package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/secret/local"
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

func TestNewUsesDiscoveryWriteTimeout(t *testing.T) {
	t.Parallel()

	application, err := New(testConfigFor(t), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if application.server.WriteTimeout != 35*time.Second {
		t.Fatalf("write timeout = %v, want 35s", application.server.WriteTimeout)
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

	const (
		databaseID       = "finance"
		password         = "dataporch-secret-canary-91f7c2"
		connectionString = "postgresql://app_reader:" + password +
			"@postgres-import-test.invalid:6543/finance?sslmode=verify-full"
	)

	cfg := initializedTestConfig(t)

	var logs bytes.Buffer

	application, err := New(cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	waitForFile(t, cfg.AdminSocketPath)

	response, err := importOverSocket(
		cfg.AdminSocketPath,
		databaseID,
		"postgres",
		connectionString,
	)
	if err != nil {
		t.Fatalf("importOverSocket() error = %v", err)
	}

	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()

	if readErr != nil {
		t.Fatalf("ReadAll() error = %v", readErr)
	}

	if closeErr != nil {
		t.Fatalf("response Body.Close() error = %v", closeErr)
	}

	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", response.StatusCode)
	}

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	definition, err := application.manager.Lookup(databaseID)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}

	assertImportedPostgresDefinition(t, cfg, definition, password)
	assertImportArtifactsDoNotContain(t, importArtifacts{
		cfg:              cfg,
		definition:       definition,
		responseBody:     responseBody,
		logs:             logs.Bytes(),
		password:         password,
		connectionString: connectionString,
	})
}

func TestAppImportDoesNotCallAdapterAuthentication(t *testing.T) {
	t.Parallel()

	cfg := initializedTestConfig(t)
	adapter := &adapterStub{kind: "postgres", parsed: connection.ParsedConnection{
		Settings: map[string]string{"host": "postgres.internal"},
		Secrets:  map[string][]byte{"password": []byte("private")},
	}}

	application, err := newWithAdapters(cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), adapter)
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
		HTTPAddress:            "127.0.0.1:0",
		ResourceLimit:          10,
		ShutdownPeriod:         time.Second,
		AdminSocketPath:        "/tmp/dataporch/admin.sock",
		MasterKeyPath:          "/tmp/dataporch/master.key",
		SecretsStorePath:       "/tmp/dataporch/secrets.store",
		ConnectionsStorePath:   "/tmp/dataporch/connections.store",
		QueryTimeout:           20 * time.Second,
		QueryResponseByteLimit: 10_485_760,
		QueryTruncationEnabled: true,
		QueryRowLimit:          1000,
	}
}

func testConfigFor(t *testing.T) config.Config {
	t.Helper()
	base := t.TempDir()

	return config.Config{
		HTTPAddress:            "127.0.0.1:0",
		ResourceLimit:          10,
		ShutdownPeriod:         time.Second,
		AdminSocketPath:        filepath.Join(base, "admin.sock"),
		MasterKeyPath:          filepath.Join(base, "master.key"),
		SecretsStorePath:       filepath.Join(base, "secrets.store"),
		ConnectionsStorePath:   filepath.Join(base, "connections.store"),
		QueryTimeout:           20 * time.Second,
		QueryResponseByteLimit: 10_485_760,
		QueryTruncationEnabled: true,
		QueryRowLimit:          1000,
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

type importArtifacts struct {
	cfg              config.Config
	definition       connection.Definition
	responseBody     []byte
	logs             []byte
	password         string
	connectionString string
}

func assertImportedPostgresDefinition(
	t *testing.T,
	cfg config.Config,
	definition connection.Definition,
	password string,
) {
	t.Helper()

	wantSettings := map[string]string{
		"username": "app_reader",
		"host":     "postgres-import-test.invalid",
		"port":     "6543",
		"database": "finance",
		"sslmode":  "verify-full",
	}
	if definition.Kind != "postgres" || !maps.Equal(definition.Settings, wantSettings) {
		t.Fatal("definition does not match the normalized Postgres settings")
	}

	passwordRef, exists := definition.SecretRefs["password"]
	if len(definition.SecretRefs) != 1 || !exists {
		t.Fatal("definition must contain a password secret reference only")
	}

	scheme, _, err := passwordRef.Parts()
	if err != nil {
		t.Fatalf("password reference Parts() error = %v", err)
	}

	if scheme != "local" {
		t.Fatalf("password reference scheme = %q, want local", scheme)
	}

	secretStore, err := local.Open(local.Paths{
		KeyPath:   cfg.MasterKeyPath,
		StorePath: cfg.SecretsStorePath,
	})
	if err != nil {
		t.Fatalf("local.Open() error = %v", err)
	}

	resolvedPassword, err := secretStore.Resolve(t.Context(), passwordRef)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	defer clear(resolvedPassword)

	wantPassword := []byte(password)
	defer clear(wantPassword)

	if !bytes.Equal(resolvedPassword, wantPassword) {
		t.Fatal("resolved password does not match imported password")
	}
}

func assertImportArtifactsDoNotContain(t *testing.T, artifacts importArtifacts) {
	t.Helper()

	definitionData, err := json.Marshal(artifacts.definition)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	persistedDefinitions, err := os.ReadFile(artifacts.cfg.ConnectionsStorePath)
	if err != nil {
		t.Fatalf("ReadFile(connections) error = %v", err)
	}

	persistedSecrets, err := os.ReadFile(artifacts.cfg.SecretsStorePath)
	if err != nil {
		t.Fatalf("ReadFile(secrets) error = %v", err)
	}

	for source, data := range map[string][]byte{
		"manager definition": definitionData,
		"connection store":   persistedDefinitions,
		"secret store":       persistedSecrets,
		"socket response":    artifacts.responseBody,
		"logs":               artifacts.logs,
	} {
		assertSensitiveValuesAbsent(
			t,
			source,
			data,
			artifacts.password,
			artifacts.connectionString,
		)
	}
}

func assertSensitiveValuesAbsent(t *testing.T, source string, data []byte, values ...string) {
	t.Helper()

	for _, value := range values {
		if bytes.Contains(data, []byte(value)) {
			t.Fatalf("%s contains sensitive connection input", source)
		}
	}
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

func importOverSocket(path, databaseID, kind, connectionString string) (*http.Response, error) {
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

	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"http://unix/v1/connections/import",
		bytes.NewReader(payload),
	)
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
