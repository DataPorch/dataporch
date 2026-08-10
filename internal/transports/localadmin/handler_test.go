package localadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adamraziv/dataporch/internal/connection"
)

func TestHandlerImportsConnection(t *testing.T) {
	t.Parallel()

	stub := &importerStub{result: connection.ImportResult{ID: "finance"}}
	handler := testHandler(t, stub)
	body := []byte(`{"databaseId":"finance","kind":"postgres","connectionString":"cHJpdmF0ZQ=="}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/connections/import", bytes.NewReader(body))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", response.Code)
	}
	if stub.got.ID != "finance" || stub.got.Kind != "postgres" || string(stub.got.ConnectionString) != "private" {
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
		{name: "unknown field", method: http.MethodPost, body: `{"databaseId":"finance","kind":"postgres","unknown":true}`, want: http.StatusBadRequest},
		{name: "trailing JSON", method: http.MethodPost, body: `{} {}`, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(tt.method, "/v1/connections/import", strings.NewReader(tt.body))
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
	request := httptest.NewRequest(http.MethodPost, "/v1/connections/import", strings.NewReader(`{"databaseId":"finance","kind":"postgres","connectionString":"cHJpdmF0ZQ=="}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), canary) {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
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
	i.got = connection.ImportRequest{ID: request.ID, Kind: request.Kind, ConnectionString: append([]byte(nil), request.ConnectionString...)}
	return i.result, i.err
}

var _ = json.Valid
