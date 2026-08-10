package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adamraziv/dataporch/internal/catalog"
)

type listerStub struct {
	resources []catalog.Resource
}

func (s listerStub) ListResources(
	_ context.Context,
	limit int,
) ([]catalog.Resource, error) {
	return s.resources[:min(limit, len(s.resources))], nil
}

func TestHandler_Health(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/healthz",
		nil,
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestHandler_ListResources(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/v1/resources?limit=1",
		nil,
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}

	var body struct {
		Resources []catalog.Resource `json:"resources"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(body.Resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(body.Resources))
	}
}

func TestHandler_ListResourcesRejectsInvalidLimit(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t)
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/v1/resources?limit=invalid",
		nil,
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	handler, err := New(
		listerStub{resources: []catalog.Resource{
			{URI: "memory://customers", Name: "Customers", Kind: "table"},
			{URI: "memory://orders", Name: "Orders", Kind: "table"},
		}},
		10,
		logger,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return handler
}
