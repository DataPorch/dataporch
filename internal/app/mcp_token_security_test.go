package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/mcptoken"
	mcpTokenLocal "github.com/adamraziv/dataporch/internal/mcptoken/local"
)

func TestApplicationMCPAuthProtectsOnlyMCPAndAllowsHealth(t *testing.T) {
	application := newAppWithPostgresRuntimeTestStub(t, testConfigFor(t), &appPostgresRuntimeTestStub{})

	mcpResponse := serveApplicationRequest(application, http.MethodGet, "/mcp", "")
	if mcpResponse.Code != http.StatusUnauthorized {
		t.Fatalf("/mcp status = %d, want %d", mcpResponse.Code, http.StatusUnauthorized)
	}
	if got := mcpResponse.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("/mcp challenge = %q, want %q", got, "Bearer")
	}

	healthResponse := serveApplicationRequest(application, http.MethodGet, "/healthz", "")
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want %d", healthResponse.Code, http.StatusOK)
	}
}

func TestApplicationMCPLoadsActiveToken(t *testing.T) {
	cfg := testConfigFor(t)
	token := seedMCPToken(t, cfg.MCPTokenStorePath)
	application := newAppWithPostgresRuntimeTestStub(t, cfg, &appPostgresRuntimeTestStub{})

	response := serveApplicationRequest(application, http.MethodPost, "/mcp", "Bearer "+token)
	if response.Code == http.StatusUnauthorized || response.Code == http.StatusServiceUnavailable {
		t.Fatalf("authenticated /mcp status = %d, want request to reach MCP handler", response.Code)
	}
	if challenge := response.Header().Get("WWW-Authenticate"); challenge != "" {
		t.Fatalf("authenticated /mcp challenge = %q, want empty", challenge)
	}
}

func TestApplicationMCPDegradedStateFailsClosedButHealthRemainsAvailable(t *testing.T) {
	cfg := testConfigFor(t)
	if err := os.WriteFile(cfg.MCPTokenStorePath, []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	application := newAppWithPostgresRuntimeTestStub(t, cfg, &appPostgresRuntimeTestStub{})

	mcpResponse := serveApplicationRequest(application, http.MethodGet, "/mcp", "Bearer dp-any-token")
	if mcpResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded /mcp status = %d, want %d", mcpResponse.Code, http.StatusServiceUnavailable)
	}
	if challenge := mcpResponse.Header().Get("WWW-Authenticate"); challenge != "" {
		t.Fatalf("degraded /mcp challenge = %q, want empty", challenge)
	}

	healthResponse := serveApplicationRequest(application, http.MethodGet, "/healthz", "")
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("degraded /healthz status = %d, want %d", healthResponse.Code, http.StatusOK)
	}
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

func serveApplicationRequest(application *App, method, path, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	application.server.Handler.ServeHTTP(response, request)
	return response
}
