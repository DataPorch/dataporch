package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewValidatesLogger(t *testing.T) {
	t.Parallel()

	if _, err := New(nil); !errors.Is(err, errLoggerRequired) {
		t.Fatalf("New(nil) error = %v, want logger validation", err)
	}
}

func TestHandlerHealth(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff header = %q, want nosniff", got)
	}

	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("health status = %q, want ok", body["status"])
	}
}

func TestHandlerRemovedResourceRoute(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/resources", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()

	handler, err := New(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return handler
}
