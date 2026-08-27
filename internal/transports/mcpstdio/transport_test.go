package mcpstdio

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestUnixHTTPClientUsesOwnerOnlyUnixSocketAndRefreshesCredential(t *testing.T) {
	t.Parallel()

	root := shortSocketDir(t)
	path := filepath.Join(root, "mcp.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	var mu sync.Mutex
	var paths, authorizations []string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		call := len(paths)
		mu.Unlock()
		if call == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
		_ = listener.Close()
	})
	go func() { _ = server.Serve(listener) }()

	credentials := &credentialSequence{values: []string{"first", "second"}}
	client := unixHTTPClient(path, credentials)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://unix/mcp", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.StatusCode)
	}
	_ = response.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if !equalStrings(paths, []string{"/mcp", "/mcp"}) {
		t.Fatalf("paths = %#v, want two /mcp requests", paths)
	}
	if !equalStrings(authorizations, []string{"Bearer first", "Bearer second"}) {
		t.Fatalf("Authorization headers = %#v, want refreshed bearer headers", authorizations)
	}
	if credentials.reads != 2 {
		t.Fatalf("credential reads = %d, want 2", credentials.reads)
	}
}

func TestUnixDialerMapsUnavailableRuntime(t *testing.T) {
	t.Parallel()

	client := unixHTTPClient(filepath.Join(t.TempDir(), "missing.sock"), &credentialSequence{values: []string{"first"}})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://unix/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	response, err := client.Do(request)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("Do() error = %v, want ErrRuntimeUnavailable", err)
	}
}

func TestNewUnixTransportDisablesSDKRetriesAndSSE(t *testing.T) {
	t.Parallel()

	transport := newUnixTransport("/tmp/mcp.sock", &credentialSequence{})
	if transport.MaxRetries >= 0 {
		t.Fatalf("MaxRetries = %d, want negative", transport.MaxRetries)
	}
	if !transport.DisableStandaloneSSE {
		t.Fatal("DisableStandaloneSSE = false, want true")
	}
}

func shortSocketDir(t *testing.T) string {
	t.Helper()
	//nolint:usetesting // Unix socket paths must remain short on macOS.
	root, err := os.MkdirTemp("/tmp", "dp-stdio-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
