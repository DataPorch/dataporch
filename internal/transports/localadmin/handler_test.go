package localadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/connection/filestore"
	"github.com/adamraziv/dataporch/internal/secret/local"
)

func TestHandlerImportsConnection(t *testing.T) {
	t.Parallel()

	stub := &importerStub{result: connection.ImportResult{ID: "finance"}}
	handler := testHandler(t, stub)
	body := []byte(`{"databaseId":"finance","kind":"postgres","connectionString":"cHJpdmF0ZQ=="}`)
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"/v1/connections/import",
		bytes.NewReader(body),
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", response.Code)
	}

	hasExpectedID := stub.got.ID == "finance"
	hasExpectedKind := stub.got.Kind == "postgres"

	hasExpectedConnectionString := string(stub.got.ConnectionString) == "private"
	if !hasExpectedID || !hasExpectedKind || !hasExpectedConnectionString {
		t.Fatalf("import request = %#v", stub.got)
	}

	if strings.Contains(response.Body.String(), "private") {
		t.Fatal("response leaked connection string")
	}
}

func TestHandlerRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	handler := testHandler(t, &importerStub{})

	tests := []struct {
		name, method, body string
		want               int
	}{
		{name: "wrong method", method: http.MethodGet, body: "", want: http.StatusMethodNotAllowed},
		{
			name:   "unknown field",
			method: http.MethodPost,
			body:   `{"databaseId":"finance","kind":"postgres","unknown":true}`,
			want:   http.StatusBadRequest,
		},
		{name: "trailing json", method: http.MethodPost, body: `{} {}`, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequestWithContext(
				t.Context(),
				tt.method,
				"/v1/connections/import",
				strings.NewReader(tt.body),
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d", response.Code, tt.want)
			}
		})
	}
}

func TestHandlerSanitizesImportError(t *testing.T) {
	t.Parallel()

	canary := "postgres://reader:password@host/database"
	handler := testHandler(t, &importerStub{err: errors.New(canary)})
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"/v1/connections/import",
		strings.NewReader(
			`{"databaseId":"finance","kind":"postgres","connectionString":"cHJpdmF0ZQ=="}`,
		),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), canary) {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestHandlerKeepsConnectionStringOutsidePersistenceAndOutput(t *testing.T) {
	t.Parallel()

	const canary = "dataporch-secret-canary-91f7c2"

	fixture := newSecretIsolationFixture(t, canary)

	payload, err := json.Marshal(struct {
		DatabaseID       connection.ID   `json:"databaseId"`
		Kind             connection.Kind `json:"kind"`
		ConnectionString []byte          `json:"connectionString"`
	}{DatabaseID: "finance", Kind: "postgres", ConnectionString: []byte(fixture.connectionString)})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(
		response,
		httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			"/v1/connections/import",
			bytes.NewReader(payload),
		),
	)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", response.Code)
	}

	secretsData, err := os.ReadFile(fixture.secretsPath)
	if err != nil {
		t.Fatalf("ReadFile(secrets) error = %v", err)
	}

	connectionsData, err := os.ReadFile(fixture.connectionsPath)
	if err != nil {
		t.Fatalf("ReadFile(connections) error = %v", err)
	}

	for name, value := range map[string][]byte{
		"encrypted secrets":    secretsData,
		"connection store":     connectionsData,
		"handler response":     response.Body.Bytes(),
		"structured log":       fixture.logs.Bytes(),
		"request return error": nil,
	} {
		if bytes.Contains(value, []byte(canary)) || bytes.Contains(value, []byte(fixture.connectionString)) {
			t.Fatalf("%s leaked connection secret", name)
		}
	}
}

type secretIsolationFixture struct {
	handler          http.Handler
	logs             *bytes.Buffer
	secretsPath      string
	connectionsPath  string
	connectionString string
}

func newSecretIsolationFixture(t *testing.T, canary string) secretIsolationFixture {
	t.Helper()

	base := t.TempDir()
	keyPath := filepath.Join(base, "master.key")
	secretsPath := filepath.Join(base, "secrets.store")
	connectionsPath := filepath.Join(base, "connections.store")

	if err := local.Init(local.Paths{KeyPath: keyPath, StorePath: secretsPath}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	secrets, err := local.Open(local.Paths{KeyPath: keyPath, StorePath: secretsPath})
	if err != nil {
		t.Fatalf("Open secrets() error = %v", err)
	}

	definitions, err := filestore.Open(connectionsPath)
	if err != nil {
		t.Fatalf("Open definitions() error = %v", err)
	}

	connector, err := connection.NewConnector(canaryAdapter{canary: canary})
	if err != nil {
		t.Fatalf("New connector() error = %v", err)
	}

	manager, err := connection.NewManager(secrets, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	importer, err := connection.NewImporter(connection.ImporterDependencies{
		Adapters:    connector,
		Secrets:     secrets,
		Definitions: definitions,
		Registrar:   manager,
	})
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}

	logs := &bytes.Buffer{}

	handler, err := NewHandler(importer, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	return secretIsolationFixture{
		handler:          handler,
		logs:             logs,
		secretsPath:      secretsPath,
		connectionsPath:  connectionsPath,
		connectionString: "postgres://reader:" + canary + "@postgres.internal/finance",
	}
}

func testHandler(t *testing.T, importer Importer) http.Handler {
	t.Helper()

	handler, err := NewHandler(importer, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	return handler
}

type importerStub struct {
	result connection.ImportResult
	err    error
	got    connection.ImportRequest
}

func (i *importerStub) Import(_ context.Context, request connection.ImportRequest) (connection.ImportResult, error) {
	i.got = connection.ImportRequest{
		ID:               request.ID,
		Kind:             request.Kind,
		ConnectionString: append([]byte(nil), request.ConnectionString...),
	}

	return i.result, i.err
}

var _ = json.Valid

type canaryAdapter struct{ canary string }

func (canaryAdapter) Kind() connection.Kind { return "postgres" }

func (a canaryAdapter) ParseConnectionString([]byte) (connection.ParsedConnection, error) {
	return connection.ParsedConnection{
		Settings: map[string]string{"host": "postgres.internal", "database": "finance", "username": "reader"},
		Secrets:  map[string][]byte{"password": []byte(a.canary)},
	}, nil
}
