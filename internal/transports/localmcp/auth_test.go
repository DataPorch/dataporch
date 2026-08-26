package localmcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticatedHandler(t *testing.T) {
	t.Parallel()

	credential := strings.Repeat("A", 43)
	tests := []struct {
		name             string
		configureRequest func(*http.Request)
		wantStatus       int
		wantChallenge    string
		wantCalls        int
	}{
		{name: "missing", wantStatus: http.StatusUnauthorized, wantChallenge: "Bearer"},
		{name: "duplicate", configureRequest: func(r *http.Request) {
			r.Header.Add("Authorization", "Bearer "+credential)
			r.Header.Add("Authorization", "Bearer "+credential)
		}, wantStatus: http.StatusUnauthorized, wantChallenge: `Bearer error="invalid_request"`},
		{name: "malformed", configureRequest: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer")
		}, wantStatus: http.StatusUnauthorized, wantChallenge: `Bearer error="invalid_request"`},
		{name: "wrong", configureRequest: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+strings.Repeat("B", 43))
		}, wantStatus: http.StatusUnauthorized, wantChallenge: `Bearer error="invalid_token"`},
		{name: "valid", configureRequest: func(r *http.Request) {
			r.Header.Set("Authorization", "bEaReR "+credential)
		}, wantStatus: http.StatusNoContent, wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			handler := authenticatedHandler(credential, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
			if test.configureRequest != nil {
				test.configureRequest(request)
			}

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			if got := recorder.Header().Get("WWW-Authenticate"); got != test.wantChallenge {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, test.wantChallenge)
			}
			if calls != test.wantCalls {
				t.Fatalf("downstream calls = %d, want %d", calls, test.wantCalls)
			}
			if strings.Contains(recorder.Body.String(), credential) {
				t.Fatalf("response body contains credential: %q", recorder.Body.String())
			}
		})
	}
}
