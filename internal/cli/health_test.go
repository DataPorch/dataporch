package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthURLUsesLoopbackForWildcardAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
		want    string
	}{
		{name: "loopback", address: "127.0.0.1:8080", want: "http://127.0.0.1:8080/healthz"},
		{name: "ipv4 wildcard", address: "0.0.0.0:8080", want: "http://127.0.0.1:8080/healthz"},
		{name: "empty host", address: ":8080", want: "http://127.0.0.1:8080/healthz"},
		{name: "ipv6 wildcard", address: "[::]:8080", want: "http://127.0.0.1:8080/healthz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := healthURL(test.address)
			if err != nil {
				t.Fatalf("healthURL() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("healthURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHealthCheckerCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantError   string
	}{
		{name: "healthy", status: http.StatusOK, contentType: "application/json", body: `{"status":"ok"}`},
		{name: "unhealthy status", status: http.StatusServiceUnavailable, contentType: "application/json", body: `{"status":"ok"}`, wantError: "unexpected status 503"},
		{name: "wrong content type", status: http.StatusOK, contentType: "text/plain", body: `{"status":"ok"}`, wantError: "expected JSON response"},
		{name: "malformed json", status: http.StatusOK, contentType: "application/json", body: "not-json", wantError: "decoding health response"},
		{name: "non-ok payload", status: http.StatusOK, contentType: "application/json", body: `{"status":"starting"}`, wantError: "status is not ok"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()

			checker := &healthChecker{
				client:         server.Client(),
				requestTimeout: time.Second,
				startupTimeout: time.Second,
				pollInterval:   time.Millisecond,
			}
			address := strings.TrimPrefix(server.URL, "http://")
			err := checker.Check(t.Context(), address)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("Check() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Check() error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestHealthCheckerCheckHonorsCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()

	checker := &healthChecker{
		client:         server.Client(),
		requestTimeout: time.Minute,
		startupTimeout: time.Minute,
		pollInterval:   time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := checker.Check(ctx, strings.TrimPrefix(server.URL, "http://"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Check() error = %v, want context.Canceled", err)
	}
}

func TestHealthCheckerWaitHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	checker := &healthChecker{
		client:         &http.Client{},
		requestTimeout: time.Second,
		startupTimeout: time.Second,
		pollInterval:   time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := checker.Wait(ctx, "127.0.0.1:8080")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
}

func TestHealthCheckerWaitHasBoundedTimeout(t *testing.T) {
	t.Parallel()

	checker := &healthChecker{
		client:         &http.Client{},
		requestTimeout: time.Millisecond,
		startupTimeout: 0,
		pollInterval:   time.Millisecond,
	}

	err := checker.Wait(t.Context(), "127.0.0.1:1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() error = %v, want context.DeadlineExceeded", err)
	}
}
