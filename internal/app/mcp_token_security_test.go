package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/mcptoken"
	mcpTokenLocal "github.com/adamraziv/dataporch/internal/mcptoken/local"
	"github.com/adamraziv/dataporch/internal/transports/localadmin"
	"github.com/adamraziv/dataporch/internal/transports/mcpauth"
)

func TestApplicationMCPAuthProtectsOnlyMCPAndAllowsHealth(t *testing.T) {
	t.Parallel()
	application := newAppWithPostgresRuntimeTestStub(t, testConfigFor(t), &appPostgresRuntimeTestStub{})

	mcpResponse := serveApplicationRequest(t, application, http.MethodGet, "/mcp", "")
	if mcpResponse.Code != http.StatusUnauthorized {
		t.Fatalf("/mcp status = %d, want %d", mcpResponse.Code, http.StatusUnauthorized)
	}

	if got := mcpResponse.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("/mcp challenge = %q, want %q", got, "Bearer")
	}

	healthResponse := serveApplicationRequest(t, application, http.MethodGet, "/healthz", "")
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want %d", healthResponse.Code, http.StatusOK)
	}
}

func TestApplicationMCPLoadsActiveToken(t *testing.T) {
	t.Parallel()
	cfg := testConfigFor(t)
	token := seedMCPToken(t, cfg.MCPTokenStorePath)
	application := newAppWithPostgresRuntimeTestStub(t, cfg, &appPostgresRuntimeTestStub{})

	response := serveApplicationRequest(t, application, http.MethodPost, "/mcp", "Bearer "+token)
	if response.Code == http.StatusUnauthorized || response.Code == http.StatusServiceUnavailable {
		t.Fatalf("authenticated /mcp status = %d, want request to reach MCP handler", response.Code)
	}

	if challenge := response.Header().Get("WWW-Authenticate"); challenge != "" {
		t.Fatalf("authenticated /mcp challenge = %q, want empty", challenge)
	}
}

func TestApplicationMCPDegradedStateFailsClosedButHealthRemainsAvailable(t *testing.T) {
	t.Parallel()

	cfg := testConfigFor(t)
	if err := os.WriteFile(cfg.MCPTokenStorePath, []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	application := newAppWithPostgresRuntimeTestStub(t, cfg, &appPostgresRuntimeTestStub{})

	mcpResponse := serveApplicationRequest(t, application, http.MethodGet, "/mcp", "Bearer dp-any-token")
	if mcpResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded /mcp status = %d, want %d", mcpResponse.Code, http.StatusServiceUnavailable)
	}

	if challenge := mcpResponse.Header().Get("WWW-Authenticate"); challenge != "" {
		t.Fatalf("degraded /mcp challenge = %q, want empty", challenge)
	}

	healthResponse := serveApplicationRequest(t, application, http.MethodGet, "/healthz", "")
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("degraded /healthz status = %d, want %d", healthResponse.Code, http.StatusOK)
	}
}

//nolint:gocyclo // The acceptance flow intentionally checks each immediate lifecycle transition.
func TestComposedMCPTokenLifecycleIsImmediate(t *testing.T) {
	t.Parallel()

	store, err := mcpTokenLocal.New(t.TempDir() + "/mcp-token.json")
	if err != nil {
		t.Fatalf("mcpTokenLocal.New() error = %v", err)
	}

	service, err := mcptoken.New(store, time.Now)
	if err != nil {
		t.Fatalf("mcptoken.New() error = %v", err)
	}

	adminHandler, err := localadmin.NewHandler(
		lifecycleImporter{},
		service,
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("localadmin.NewHandler() error = %v", err)
	}

	downstreamCalls := 0

	mcpHandler, err := mcpauth.New(
		service,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			downstreamCalls++

			w.WriteHeader(http.StatusNoContent)
		}),
	)
	if err != nil {
		t.Fatalf("mcpauth.New() error = %v", err)
	}

	initial := serveMCPTokenLifecycleRequest(t, mcpHandler, http.MethodGet, "/mcp", "")
	if initial.Code != http.StatusUnauthorized || initial.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("initial MCP response = %d/%q, want 401/Bearer", initial.Code, initial.Header().Get("WWW-Authenticate"))
	}

	created := serveMCPTokenLifecycleRequest(t, adminHandler, http.MethodPost, "/v1/mcp-token", "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", created.Code, http.StatusCreated)
	}

	firstToken := decodeMCPTokenLifecycleResponse(t, created.Body.Bytes())

	firstAccepted := serveMCPTokenLifecycleRequest(t, mcpHandler, http.MethodGet, "/mcp", firstToken)
	if firstAccepted.Code != http.StatusNoContent || downstreamCalls != 1 {
		t.Fatalf("first token response/calls = %d/%d, want 204/1", firstAccepted.Code, downstreamCalls)
	}

	rotated := serveMCPTokenLifecycleRequest(t, adminHandler, http.MethodPost, "/v1/mcp-token/rotate", "")
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate status = %d, want %d", rotated.Code, http.StatusOK)
	}

	secondToken := decodeMCPTokenLifecycleResponse(t, rotated.Body.Bytes())
	if secondToken == firstToken {
		t.Fatal("rotate returned the same token")
	}

	oldResponse := serveMCPTokenLifecycleRequest(t, mcpHandler, http.MethodGet, "/mcp", firstToken)
	if oldResponse.Code != http.StatusUnauthorized || oldResponse.Header().Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
		t.Fatalf("old token response = %d/%q, want 401/invalid_token", oldResponse.Code, oldResponse.Header().Get("WWW-Authenticate"))
	}

	newResponse := serveMCPTokenLifecycleRequest(t, mcpHandler, http.MethodGet, "/mcp", secondToken)
	if newResponse.Code != http.StatusNoContent || downstreamCalls != 2 {
		t.Fatalf("new token response/calls = %d/%d, want 204/2", newResponse.Code, downstreamCalls)
	}

	revoked := serveMCPTokenLifecycleRequest(t, adminHandler, http.MethodDelete, "/v1/mcp-token", "")
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want %d", revoked.Code, http.StatusNoContent)
	}

	final := serveMCPTokenLifecycleRequest(t, mcpHandler, http.MethodGet, "/mcp", secondToken)
	if final.Code != http.StatusUnauthorized || final.Header().Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
		t.Fatalf("revoked token response = %d/%q, want 401/invalid_token", final.Code, final.Header().Get("WWW-Authenticate"))
	}
}

func serveMCPTokenLifecycleRequest(t *testing.T, handler http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(nil))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	return response
}

func decodeMCPTokenLifecycleResponse(t *testing.T, data []byte) string {
	t.Helper()

	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("token response JSON error = %v", err)
	}

	if response.Token == "" {
		t.Fatal("token response omitted token")
	}

	return response.Token
}

type lifecycleImporter struct{}

func (lifecycleImporter) Import(context.Context, connection.ImportRequest) (connection.ImportResult, error) {
	return connection.ImportResult{}, errors.New("not used")
}

func seedMCPToken(t *testing.T, path string) string {
	t.Helper()

	store, err := mcpTokenLocal.New(path)
	if err != nil {
		t.Fatalf("mcpTokenLocal.New() error = %v", err)
	}

	service, err := mcptoken.New(store, time.Now)
	if err != nil {
		t.Fatalf("mcptoken.New() error = %v", err)
	}

	token, _, err := service.Create(t.Context())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	return token
}

func serveApplicationRequest(t *testing.T, application *App, method, path, authorization string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}

	response := httptest.NewRecorder()
	application.server.Handler.ServeHTTP(response, request)

	return response
}
