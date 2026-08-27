package localmcp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type recordingCredentialStore struct {
	publishValues []string
	deleteCalls   int
	publishErr    error
	deleteErr     error
	publishDone   chan struct{}
}

func (s *recordingCredentialStore) Publish(credential string) error {
	s.publishValues = append(s.publishValues, credential)
	if s.publishDone != nil {
		close(s.publishDone)
	}
	return s.publishErr
}

func (s *recordingCredentialStore) Delete() error {
	s.deleteCalls++
	return s.deleteErr
}

func TestNewServerValidatesDependencies(t *testing.T) {
	t.Parallel()

	valid := Dependencies{
		SocketPath:  "/tmp/dataporch.sock",
		Credentials: &recordingCredentialStore{},
		Random:      strings.NewReader(strings.Repeat("A", 32)),
		Handler:     http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}
	tests := []struct {
		name string
		edit func(*Dependencies)
	}{
		{name: "missing socket", edit: func(d *Dependencies) { d.SocketPath = "" }},
		{name: "missing credentials", edit: func(d *Dependencies) { d.Credentials = nil }},
		{name: "missing random", edit: func(d *Dependencies) { d.Random = nil }},
		{name: "missing handler", edit: func(d *Dependencies) { d.Handler = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dependencies := valid
			test.edit(&dependencies)
			if _, err := NewServer(dependencies); err == nil {
				t.Fatal("NewServer() error = nil, want validation error")
			}
		})
	}
}

//nolint:gocyclo // The lifecycle test covers publication, routing, auth, and cleanup.
func TestServerPublishesRestrictedSocketAndCleansUp(t *testing.T) {
	t.Parallel()

	root := secureTempDir(t)
	socketPath := filepath.Join(root, "mcp.sock")
	store := &recordingCredentialStore{publishDone: make(chan struct{})}
	requests := make(chan *http.Request, 1)
	server, err := NewServer(Dependencies{
		SocketPath:  socketPath,
		Credentials: store,
		Random:      strings.NewReader(strings.Repeat("A", 32)),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests <- r
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx) }()
	select {
	case <-store.publishDone:
	case <-time.After(time.Second):
		t.Fatal("Publish() was not called")
	}
	select {
	case err := <-runErr:
		t.Fatalf("Run() exited before socket creation: %v", err)
	default:
	}
	waitForSocket(t, socketPath, runErr)

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("Stat(socket) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 600", got)
	}
	if len(store.publishValues) != 1 {
		t.Fatalf("Publish() calls = %d, want 1", len(store.publishValues))
	}

	client := unixHTTPClient(t, socketPath)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://unix/mcp", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST(/mcp) error = %v", err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST(/mcp) status = %d, want 401", response.StatusCode)
	}
	_ = response.Body.Close()

	request, err = http.NewRequestWithContext(t.Context(), http.MethodPost, "http://unix/mcp", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+store.publishValues[0])
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("authenticated request error = %v", err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("authenticated status = %d, want 204", response.StatusCode)
	}
	_ = response.Body.Close()
	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("authenticated request did not reach downstream")
	}

	for _, path := range []string{"/v1/mcp-token", "/healthz", "/"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://unix"+path, nil)
		if err != nil {
			t.Fatalf("NewRequest(%s) error = %v", path, err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("GET(%s) error = %v", path, err)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("GET(%s) status = %d, want 404", path, response.StatusCode)
		}
		_ = response.Body.Close()
	}

	client.CloseIdleConnections()
	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
	if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(cleaned socket) error = %v, want not exist", err)
	}
	if store.deleteCalls != 1 {
		t.Fatalf("Delete() calls = %d, want 1", store.deleteCalls)
	}
}

func TestServerRejectsPublicationFailureWithoutSocket(t *testing.T) {
	t.Parallel()

	root := secureTempDir(t)
	socketPath := filepath.Join(root, "mcp.sock")
	store := &recordingCredentialStore{publishErr: errors.New("publish failed")}
	server, err := NewServer(Dependencies{
		SocketPath:  socketPath,
		Credentials: store,
		Random:      strings.NewReader(strings.Repeat("A", 32)),
		Handler:     http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.Run(t.Context()); err == nil {
		t.Fatal("Run() error = nil, want publication failure")
	}
	if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(socket) error = %v, want not exist", err)
	}
}

func TestServerRejectsUnsafeExistingSocketPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{name: "regular file", setup: func(t *testing.T, path string) {
			t.Helper()
			writeTestPath(t, path, 0o600)
		}},
		{name: "symlink", setup: func(t *testing.T, path string) {
			t.Helper()
			target := path + "-target"
			writeTestPath(t, target, 0o600)
			if err := os.Symlink(target, path); err != nil {
				t.Fatalf("Symlink() error = %v", err)
			}
		}},
		{name: "directory", setup: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatalf("Mkdir() error = %v", err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := secureTempDir(t)
			path := filepath.Join(root, "mcp.sock")
			test.setup(t, path)
			store := &recordingCredentialStore{}
			server, err := NewServer(Dependencies{
				SocketPath: path, Credentials: store,
				Random:  strings.NewReader(strings.Repeat("A", 32)),
				Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			})
			if err != nil {
				t.Fatalf("NewServer() error = %v", err)
			}
			if err := server.Run(t.Context()); err == nil {
				t.Fatal("Run() error = nil, want unsafe-path error")
			}
		})
	}
}

func TestServerRejectsActiveSocketAndRemovesStaleSocket(t *testing.T) {
	t.Parallel()

	root := secureTempDir(t)
	path := filepath.Join(root, "mcp.sock")
	active, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("Listen(active) error = %v", err)
	}
	server, err := NewServer(Dependencies{
		SocketPath: path, Credentials: &recordingCredentialStore{},
		Random:  strings.NewReader(strings.Repeat("A", 32)),
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("Run(active) error = %v, want already-active error", err)
	}
	if err := active.Close(); err != nil {
		t.Fatalf("Close(active) error = %v", err)
	}

	store := &recordingCredentialStore{publishDone: make(chan struct{})}
	server, err = NewServer(Dependencies{
		SocketPath: path, Credentials: store,
		Random:  strings.NewReader(strings.Repeat("B", 32)),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	})
	if err != nil {
		t.Fatalf("NewServer(stale) error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx) }()
	waitForSocket(t, path, runErr)
	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run(stale) error = %v", err)
	}
}

func TestServerReservesSocketBeforePublishingCredential(t *testing.T) {
	t.Parallel()

	root := secureTempDir(t)
	path := filepath.Join(root, "mcp.sock")
	firstStore := &recordingCredentialStore{publishDone: make(chan struct{})}
	first, err := NewServer(Dependencies{
		SocketPath: path, Credentials: firstStore,
		Random:  strings.NewReader(strings.Repeat("A", 32)),
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if err != nil {
		t.Fatalf("NewServer(first) error = %v", err)
	}
	secondStore := &recordingCredentialStore{}
	second, err := NewServer(Dependencies{
		SocketPath: path, Credentials: secondStore,
		Random:  strings.NewReader(strings.Repeat("B", 32)),
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if err != nil {
		t.Fatalf("NewServer(second) error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	firstRun := make(chan error, 1)
	go func() { firstRun <- first.Run(ctx) }()
	select {
	case <-firstStore.publishDone:
	case <-time.After(time.Second):
		t.Fatal("first server did not publish its credential")
	}
	waitForSocket(t, path, firstRun)

	secondRun := make(chan error, 1)
	go func() { secondRun <- second.Run(t.Context()) }()
	select {
	case err := <-secondRun:
		if err == nil || !strings.Contains(err.Error(), "already active") {
			t.Fatalf("second Run() error = %v, want already-active error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Run() did not reject the active socket")
	}
	if len(secondStore.publishValues) != 0 {
		t.Fatalf("second Publish() calls = %d, want 0", len(secondStore.publishValues))
	}

	cancel()
	if err := <-firstRun; err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
}

func TestServerKeepsRuntimeOwnershipUntilCleanupCompletes(t *testing.T) {
	t.Parallel()

	root := secureTempDir(t)
	path := filepath.Join(root, "mcp.sock")
	firstStore := &recordingCredentialStore{publishDone: make(chan struct{})}
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	handlerDone := make(chan struct{})
	first, err := NewServer(Dependencies{
		SocketPath: path, Credentials: firstStore,
		Random: strings.NewReader(strings.Repeat("A", 32)),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(handlerStarted)
			<-releaseHandler
			close(handlerDone)
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	if err != nil {
		t.Fatalf("NewServer(first) error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	firstRun := make(chan error, 1)
	go func() { firstRun <- first.Run(ctx) }()
	select {
	case <-firstStore.publishDone:
	case <-time.After(time.Second):
		t.Fatal("first server did not publish its credential")
	}
	waitForSocket(t, path, firstRun)

	client := unixHTTPClient(t, path)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://unix/mcp", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+firstStore.publishValues[0])
	responseDone := make(chan error, 1)
	go func() {
		response, requestErr := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		responseDone <- requestErr
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	cancel()
	waitForClosedSocket(t, path, firstRun)

	secondStore := &recordingCredentialStore{}
	second, err := NewServer(Dependencies{
		SocketPath: path, Credentials: secondStore,
		Random:  strings.NewReader(strings.Repeat("B", 32)),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	})
	if err != nil {
		t.Fatalf("NewServer(second) error = %v", err)
	}
	secondRun := make(chan error, 1)
	go func() { secondRun <- second.Run(t.Context()) }()
	select {
	case err := <-secondRun:
		if !errors.Is(err, errRuntimeAlreadyActive) {
			t.Fatalf("second Run() error = %v, want active-runtime error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Run() did not preserve first runtime ownership")
	}
	if len(secondStore.publishValues) != 0 {
		t.Fatalf("second Publish() calls = %d, want 0", len(secondStore.publishValues))
	}

	close(releaseHandler)
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish")
	}
	select {
	case err := <-responseDone:
		if err != nil {
			t.Fatalf("request error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not finish")
	}
	if err := <-firstRun; err != nil {
		t.Fatalf("first Run() error = %v", err)
	}

	secondStore.publishDone = make(chan struct{})
	secondContext, secondCancel := context.WithCancel(t.Context())
	defer secondCancel()
	secondRun = make(chan error, 1)
	go func() { secondRun <- second.Run(secondContext) }()
	select {
	case <-secondStore.publishDone:
	case <-time.After(time.Second):
		t.Fatal("second server did not publish after first cleanup")
	}
	waitForSocket(t, path, secondRun)
	secondCancel()
	if err := <-secondRun; err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
}

//nolint:gocyclo // The test coordinates server, request, and shutdown lifecycles.
func TestServerWaitsForGracefulShutdown(t *testing.T) {
	t.Parallel()

	root := secureTempDir(t)
	path := filepath.Join(root, "mcp.sock")
	store := &recordingCredentialStore{publishDone: make(chan struct{})}
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	handlerDone := make(chan struct{})
	server, err := NewServer(Dependencies{
		SocketPath: path, Credentials: store,
		Random: strings.NewReader(strings.Repeat("A", 32)),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(handlerStarted)
			<-releaseHandler
			close(handlerDone)
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx) }()
	select {
	case <-store.publishDone:
	case <-time.After(time.Second):
		t.Fatal("Publish() was not called")
	}
	waitForSocket(t, path, runErr)

	client := unixHTTPClient(t, path)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://unix/mcp", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+store.publishValues[0])
	responseDone := make(chan error, 1)
	go func() {
		response, requestErr := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		responseDone <- requestErr
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	cancel()
	select {
	case err := <-runErr:
		close(releaseHandler)
		<-handlerDone
		t.Fatalf("Run() returned before handler drained: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseHandler)
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish")
	}
	select {
	case err := <-responseDone:
		if err != nil {
			t.Fatalf("request error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not finish")
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not finish after handler drained")
	}
}

func TestServerForceClosesHandlersAfterShutdownTimeout(t *testing.T) {
	t.Parallel()

	root := secureTempDir(t)
	path := filepath.Join(root, "mcp.sock")
	store := &recordingCredentialStore{publishDone: make(chan struct{})}
	handlerStarted := make(chan struct{})
	handlerCanceled := make(chan struct{})
	server, err := NewServer(Dependencies{
		SocketPath: path, Credentials: store,
		Random: strings.NewReader(strings.Repeat("A", 32)),
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(handlerStarted)
			<-request.Context().Done()
			close(handlerCanceled)
		}),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	server.shutdownTimeout = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx) }()
	select {
	case <-store.publishDone:
	case <-time.After(time.Second):
		t.Fatal("Publish() was not called")
	}
	waitForSocket(t, path, runErr)

	client := unixHTTPClient(t, path)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://unix/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+store.publishValues[0])
	responseDone := make(chan error, 1)
	go func() {
		response, requestErr := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		responseDone <- requestErr
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	cancel()
	select {
	case <-handlerCanceled:
	case <-time.After(time.Second):
		t.Fatal("handler was not canceled after shutdown timeout")
	}
	select {
	case err := <-runErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run() error = %v, want shutdown deadline error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not finish after forced close")
	}
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		t.Fatal("request did not finish after forced close")
	}
}

func TestServerRotatesCredentialAcrossRuns(t *testing.T) {
	t.Parallel()

	root := secureTempDir(t)
	path := filepath.Join(root, "mcp.sock")
	first := runServerOnce(t, path, bytesReader('A'))
	second := runServerOnce(t, path, bytesReader('B'))
	if first == second {
		t.Fatal("successive runs reused the local MCP credential")
	}
}

func TestServerDoesNotPublishAfterCancellation(t *testing.T) {
	t.Parallel()

	root := secureTempDir(t)
	store := &recordingCredentialStore{}
	server, err := NewServer(Dependencies{
		SocketPath: filepath.Join(root, "mcp.sock"), Credentials: store,
		Random:  strings.NewReader(strings.Repeat("A", 32)),
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := server.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if len(store.publishValues) != 0 || store.deleteCalls != 0 {
		t.Fatalf("credential lifecycle = publish %d/delete %d, want no artifacts", len(store.publishValues), store.deleteCalls)
	}
}

func runServerOnce(t *testing.T, path string, random io.Reader) string {
	t.Helper()
	store := &recordingCredentialStore{publishDone: make(chan struct{})}
	server, err := NewServer(Dependencies{
		SocketPath: path, Credentials: store, Random: random,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx) }()
	waitForSocket(t, path, runErr)
	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return store.publishValues[0]
}

func bytesReader(value byte) io.Reader {
	return strings.NewReader(strings.Repeat(string(value), 32))
}

func writeTestPath(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fixture"), mode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
}

func secureTempDir(t *testing.T) string {
	t.Helper()
	//nolint:usetesting // Unix socket paths must remain short on macOS.
	root, err := os.MkdirTemp("", "dp-local-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	return root
}

func waitForSocket(t *testing.T, path string, runErr <-chan error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-runErr:
			t.Fatalf("Run() exited before socket creation: %v", err)
		default:
		}
		if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 && info.Mode().Perm() == 0o600 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("socket %q was not created", path)
}

func waitForClosedSocket(t *testing.T, path string, runErr <-chan error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-runErr:
			t.Fatalf("Run() exited before cleanup completed: %v", err)
		default:
		}

		connection, err := net.DialTimeout("unix", path, 20*time.Millisecond) //nolint:noctx // The probe is deliberately bounded.
		if connection != nil {
			_ = connection.Close()
			time.Sleep(time.Millisecond)
			continue
		}
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return
		}
		t.Fatalf("dialing local MCP socket during shutdown: %v", err)
	}
	t.Fatalf("socket %q was not closed", path)
}

func unixHTTPClient(t *testing.T, socketPath string) *http.Client {
	t.Helper()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}
