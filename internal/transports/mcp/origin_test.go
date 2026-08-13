package mcp

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithOriginValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		method       string
		origins      []string
		secFetchSite string
		wantStatus   int
		wantCalls    int
	}{
		{
			name:       "absent origin",
			method:     http.MethodGet,
			wantStatus: http.StatusNoContent,
			wantCalls:  1,
		},
		{
			name:       "same host on safe method",
			method:     http.MethodGet,
			origins:    []string{"http://127.0.0.1:8080"},
			wantStatus: http.StatusNoContent,
			wantCalls:  1,
		},
		{
			name:       "same host on post",
			method:     http.MethodPost,
			origins:    []string{"http://127.0.0.1:8080"},
			wantStatus: http.StatusNoContent,
			wantCalls:  1,
		},
		{
			name:       "foreign origin on safe method",
			method:     http.MethodGet,
			origins:    []string{"https://attacker.example"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "foreign origin on post",
			method:     http.MethodPost,
			origins:    []string{"https://attacker.example"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:         "foreign origin cannot claim same fetch site",
			method:       http.MethodPost,
			origins:      []string{"https://attacker.example"},
			secFetchSite: "same-origin",
			wantStatus:   http.StatusForbidden,
		},
		{
			name:       "null origin",
			method:     http.MethodPost,
			origins:    []string{"null"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "malformed origin",
			method:     http.MethodPost,
			origins:    []string{"://"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "origin with user information",
			method:     http.MethodPost,
			origins:    []string{"http://user@127.0.0.1:8080"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "origin with path",
			method:     http.MethodPost,
			origins:    []string{"http://127.0.0.1:8080/path"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "origin with empty query delimiter",
			method:     http.MethodPost,
			origins:    []string{"http://127.0.0.1:8080?"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "origin with empty fragment delimiter",
			method:     http.MethodPost,
			origins:    []string{"http://127.0.0.1:8080#"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "origin with empty port",
			method:     http.MethodPost,
			origins:    []string{"http://127.0.0.1:"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "empty present origin",
			method:     http.MethodPost,
			origins:    []string{""},
			wantStatus: http.StatusForbidden,
		},
		{
			name:   "multiple origins",
			method: http.MethodPost,
			origins: []string{
				"http://127.0.0.1:8080",
				"https://attacker.example",
			},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(
				tt.method,
				"http://127.0.0.1:8080/mcp",
				nil,
			)
			for _, origin := range tt.origins {
				request.Header.Add("Origin", origin)
			}
			if tt.secFetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", tt.secFetchSite)
			}

			calls := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusNoContent)
			})
			handler := withOriginValidation(
				http.NewCrossOriginProtection(),
				next,
			)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if calls != tt.wantCalls {
				t.Errorf("next calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestHandlerRejectsInvalidOriginsForEveryMethod(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	handler, err := New(newMCPTestDependencies(logger))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	methods := []string{
		http.MethodGet,
		http.MethodHead,
		http.MethodPost,
		http.MethodDelete,
		http.MethodOptions,
	}
	for _, method := range methods {
		t.Run(strings.ToLower(method), func(t *testing.T) {
			t.Parallel()

			request, err := http.NewRequestWithContext(
				t.Context(),
				method,
				server.URL,
				strings.NewReader("{"),
			)
			if err != nil {
				t.Fatalf("NewRequestWithContext() error = %v", err)
			}
			request.Header.Set("Origin", "https://attacker.example")

			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatalf("Body.Close() error = %v", err)
			}

			if response.StatusCode != http.StatusForbidden {
				t.Errorf(
					"status = %d, want %d",
					response.StatusCode,
					http.StatusForbidden,
				)
			}
		})
	}
}
