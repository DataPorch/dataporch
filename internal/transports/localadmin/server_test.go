package localadmin

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerCreatesRestrictedSocket(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "admin.sock")
	server, err := NewServer(
		path,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	waitForSocket(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o660 {
		t.Errorf("socket mode = %o, want 660", info.Mode().Perm())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestServerServesHTTPOverUnixSocket(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "admin.sock")
	server, err := NewServer(
		path,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	waitForSocket(t, path)
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("GET / HTTP/1.1\r\nHost: unix\r\n\r\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	buffer := make([]byte, 128)
	size, err := connection.Read(buffer)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !bytes.Contains(buffer[:size], []byte("204 No Content")) {
		t.Fatalf("response = %q", buffer[:size])
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestServerRemovesSocketOnShutdown(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "admin.sock")
	server, err := NewServer(path, http.NotFoundHandler(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	waitForSocket(t, path)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat() error = %v, want not exist", err)
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("socket %s was not created", path)
}
