package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/mcptoken"
)

//nolint:gocyclo // The table exercises each client operation and its wire contract.
func TestMCPTokenClientRequests(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 8, 14, 8, 50, 0, 0, time.UTC)
	rotatedAt := createdAt.Add(time.Hour)

	tests := []struct {
		name       string
		method     string
		path       string
		response   string
		statusCode int
		call       func(*unixClient) error
	}{
		{
			name:       "create",
			method:     http.MethodPost,
			path:       "/v1/mcp-token",
			response:   fmt.Sprintf(`{"token":"dp-created","metadata":{"created_at":%q,"rotated_at":null}}`, createdAt.Format(time.RFC3339)),
			statusCode: http.StatusCreated,
			call: func(client *unixClient) error {
				token, metadata, err := client.CreateMCPToken(context.Background())
				if err != nil {
					return err
				}

				if token != "dp-created" || !metadata.CreatedAt.Equal(createdAt) || metadata.RotatedAt != nil {
					return fmt.Errorf("result = %q/%#v", token, metadata)
				}

				return nil
			},
		},
		{
			name:       "status",
			method:     http.MethodGet,
			path:       "/v1/mcp-token",
			response:   fmt.Sprintf(`{"state":"active","metadata":{"created_at":%q,"rotated_at":%q}}`, createdAt.Format(time.RFC3339), rotatedAt.Format(time.RFC3339)),
			statusCode: http.StatusOK,
			call: func(client *unixClient) error {
				status, err := client.MCPTokenStatus(context.Background())
				if err != nil {
					return err
				}

				if status.State != mcptoken.StateActive || !status.Metadata.CreatedAt.Equal(createdAt) ||
					status.Metadata.RotatedAt == nil || !status.Metadata.RotatedAt.Equal(rotatedAt) {
					return fmt.Errorf("status = %#v", status)
				}

				return nil
			},
		},
		{
			name:       "rotate",
			method:     http.MethodPost,
			path:       "/v1/mcp-token/rotate",
			response:   fmt.Sprintf(`{"token":"dp-rotated","metadata":{"created_at":%q,"rotated_at":%q}}`, createdAt.Format(time.RFC3339), rotatedAt.Format(time.RFC3339)),
			statusCode: http.StatusOK,
			call: func(client *unixClient) error {
				token, metadata, err := client.RotateMCPToken(context.Background())
				if err != nil {
					return err
				}

				if token != "dp-rotated" || !metadata.CreatedAt.Equal(createdAt) ||
					metadata.RotatedAt == nil || !metadata.RotatedAt.Equal(rotatedAt) {
					return fmt.Errorf("result = %q/%#v", token, metadata)
				}

				return nil
			},
		},
		{
			name:       "revoke",
			method:     http.MethodDelete,
			path:       "/v1/mcp-token",
			statusCode: http.StatusNoContent,
			call: func(client *unixClient) error {
				return client.RevokeMCPToken(context.Background())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotMethod, gotPath string

			path := startSocketHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path

				if tt.response != "" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.statusCode)
					_, _ = w.Write([]byte(tt.response))

					return
				}

				w.WriteHeader(tt.statusCode)
			}))

			client, err := newUnixClient(path)
			if err != nil {
				t.Fatalf("newUnixClient() error = %v", err)
			}

			if err := tt.call(client); err != nil {
				t.Fatalf("client call error = %v", err)
			}

			if gotMethod != tt.method || gotPath != tt.path {
				t.Fatalf("request = %s %s, want %s %s", gotMethod, gotPath, tt.method, tt.path)
			}
		})
	}
}

func TestMCPTokenClientMapsSafeErrors(t *testing.T) {
	t.Parallel()

	canary := "dp-client-error-canary"
	tests := []struct {
		name       string
		statusCode int
		code       string
		call       func(*unixClient) error
		wantPart   string
	}{
		{
			name:       "already configured",
			statusCode: http.StatusConflict,
			code:       "token_exists",
			call: func(client *unixClient) error {
				_, _, err := client.CreateMCPToken(context.Background())
				return err
			},
			wantPart: "token_exists",
		},
		{
			name:       "not configured",
			statusCode: http.StatusConflict,
			code:       "token_not_configured",
			call: func(client *unixClient) error {
				_, _, err := client.RotateMCPToken(context.Background())
				return err
			},
			wantPart: "token_not_configured",
		},
		{
			name:       "unavailable",
			statusCode: http.StatusServiceUnavailable,
			code:       "token_unavailable",
			call: func(client *unixClient) error {
				return client.RevokeMCPToken(context.Background())
			},
			wantPart: "token_unavailable",
		},
		{
			name:       "unknown code",
			statusCode: http.StatusInternalServerError,
			code:       "secret-details",
			call: func(client *unixClient) error {
				return client.RevokeMCPToken(context.Background())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := startSocketHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_, _ = fmt.Fprintf(w, `{"code":%q,"message":%q}`, tt.code, canary)
			}))

			client, err := newUnixClient(path)
			if err != nil {
				t.Fatalf("newUnixClient() error = %v", err)
			}

			err = tt.call(client)
			if err == nil {
				t.Fatal("client call error = nil, want error")
			}

			if tt.wantPart != "" && !strings.Contains(err.Error(), tt.wantPart) {
				t.Fatalf("error = %q, want code %q", err, tt.wantPart)
			}

			if strings.Contains(err.Error(), canary) {
				t.Fatalf("error leaked response detail: %q", err)
			}
		})
	}
}

func TestMCPTokenClientBoundsResponses(t *testing.T) {
	t.Parallel()
	path := startSocketHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"state":"active","metadata":{"created_at":"2026-08-14T08:50:00Z","padding":%q}}`, strings.Repeat("x", mcpTokenResponseLimit))
	}))

	client, err := newUnixClient(path)
	if err != nil {
		t.Fatalf("newUnixClient() error = %v", err)
	}

	_, err = client.MCPTokenStatus(context.Background())
	if err == nil || strings.Contains(err.Error(), strings.Repeat("x", 32)) {
		t.Fatalf("MCPTokenStatus() error = %v, want bounded safe error", err)
	}
}

func TestMCPTokenClientRejectsSensitiveInvalidSuccess(t *testing.T) {
	t.Parallel()

	canary := "dp-sensitive-success-canary"
	path := startSocketHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"token":%q,"metadata":{}}`, canary)
	}))

	client, err := newUnixClient(path)
	if err != nil {
		t.Fatalf("newUnixClient() error = %v", err)
	}

	_, _, err = client.CreateMCPToken(context.Background())
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("CreateMCPToken() error = %v, want safe validation error", err)
	}
}

type testMCPTokenManager struct{}

func (*testMCPTokenManager) Create(context.Context) (string, mcptoken.Metadata, error) {
	return "", mcptoken.Metadata{}, nil
}

func (*testMCPTokenManager) Status() mcptoken.Status {
	return mcptoken.Status{State: mcptoken.StateNone}
}

func (*testMCPTokenManager) Rotate(context.Context) (string, mcptoken.Metadata, error) {
	return "", mcptoken.Metadata{}, nil
}

func (*testMCPTokenManager) Revoke(context.Context) error { return nil }
