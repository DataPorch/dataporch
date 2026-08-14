package mcpauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/adamraziv/dataporch/internal/mcptoken"
)

type verifierFunc func(string) error

func (f verifierFunc) Verify(token string) error {
	return f(token)
}

func TestNewValidatesDependencies(t *testing.T) {
	t.Parallel()

	validVerifier := verifierFunc(func(string) error { return nil })
	validNext := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	tests := []struct {
		name     string
		verifier Verifier
		next     http.Handler
	}{
		{name: "nil verifier", next: validNext},
		{name: "nil downstream", verifier: validVerifier},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := New(tt.verifier, tt.next); err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
		})
	}
}

//nolint:funlen // The table covers every approved authentication response and downstream gate.
func TestHandlerAuthorization(t *testing.T) {
	t.Parallel()

	const validToken = "dp-valid-token"

	tests := []struct {
		name             string
		configureRequest func(*http.Request)
		verifyError      error
		wantStatus       int
		wantChallenge    string
		wantCalls        int
	}{
		{
			name:          "missing header",
			wantStatus:    http.StatusUnauthorized,
			wantChallenge: "Bearer",
		},
		{
			name: "duplicate headers",
			configureRequest: func(r *http.Request) {
				r.Header.Add("Authorization", "Bearer "+validToken)
				r.Header.Add("Authorization", "Bearer "+validToken)
			},
			wantStatus:    http.StatusUnauthorized,
			wantChallenge: `Bearer error="invalid_request"`,
		},
		{
			name: "empty bearer value",
			configureRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer")
			},
			wantStatus:    http.StatusUnauthorized,
			wantChallenge: `Bearer error="invalid_request"`,
		},
		{
			name: "extra fields",
			configureRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+validToken+" extra")
			},
			wantStatus:    http.StatusUnauthorized,
			wantChallenge: `Bearer error="invalid_request"`,
		},
		{
			name: "non bearer scheme",
			configureRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Basic "+validToken)
			},
			wantStatus:    http.StatusUnauthorized,
			wantChallenge: `Bearer error="invalid_request"`,
		},
		{
			name: "wrong token",
			configureRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer wrong-token")
			},
			verifyError:   mcptoken.ErrInvalidToken,
			wantStatus:    http.StatusUnauthorized,
			wantChallenge: `Bearer error="invalid_token"`,
		},
		{
			name: "no token runtime",
			configureRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+validToken)
			},
			verifyError:   mcptoken.ErrNoToken,
			wantStatus:    http.StatusUnauthorized,
			wantChallenge: `Bearer error="invalid_token"`,
		},
		{
			name: "degraded runtime",
			configureRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+validToken)
			},
			verifyError: mcptoken.ErrUnavailable,
			wantStatus:  http.StatusServiceUnavailable,
		},
		{
			name: "unknown verifier failure",
			configureRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+validToken)
			},
			verifyError: errors.New("unexpected verifier failure"),
			wantStatus:  http.StatusServiceUnavailable,
		},
		{
			name: "case insensitive bearer",
			configureRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "bEaReR "+validToken)
			},
			wantStatus: http.StatusNoContent,
			wantCalls:  1,
		},
		{
			name: "valid token",
			configureRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+validToken)
			},
			wantStatus: http.StatusNoContent,
			wantCalls:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			verifier := verifierFunc(func(token string) error {
				if token != validToken {
					return mcptoken.ErrInvalidToken
				}

				return tt.verifyError
			})
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++

				w.WriteHeader(http.StatusNoContent)
			})

			handler, err := New(verifier, next)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/mcp", nil)
			if tt.configureRequest != nil {
				tt.configureRequest(req)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tt.wantStatus)
			}

			if got := rr.Header().Get("WWW-Authenticate"); got != tt.wantChallenge {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, tt.wantChallenge)
			}

			if calls != tt.wantCalls {
				t.Fatalf("downstream calls = %d, want %d", calls, tt.wantCalls)
			}

			if strings.Contains(rr.Body.String(), validToken) {
				t.Fatalf("response body contains credential: %q", rr.Body.String())
			}
		})
	}
}

func TestHandlerDoesNotUseAlternateTokenSources(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	handler, err := New(
		verifierFunc(func(string) error { return nil }),
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for _, source := range []string{"query", "cookie"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/mcp?access_token=dp-token", nil)
			req.AddCookie(&http.Cookie{Name: "access_token", Value: "dp-token"})

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
			}

			if got := rr.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, "Bearer")
			}
		})
	}

	if got := calls.Load(); got != 0 {
		t.Fatalf("downstream calls = %d, want 0", got)
	}
}
