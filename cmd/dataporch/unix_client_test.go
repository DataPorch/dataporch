package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/transports/localadmin"
)

func TestUnixClientImportsThroughLocalAdminSocket(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "admin.sock")
	importer := &socketImporter{result: connection.ImportResult{ID: "finance", Updated: true}}
	handler, err := localadmin.NewHandler(importer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server, err := localadmin.NewServer(path, handler, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	}()
	waitForSocket(t, path)

	client, err := newUnixClient(path)
	if err != nil {
		t.Fatalf("newUnixClient() error = %v", err)
	}
	result, err := client.Import(context.Background(), connection.ImportRequest{
		ID:               "finance",
		Kind:             "postgres",
		ConnectionString: []byte("postgres://reader:password@host/finance"),
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if !result.Updated || result.ID != "finance" || result.ConnectionTested {
		t.Fatalf("Import() result = %#v", result)
	}
	if importer.got.ID != "finance" || importer.got.Kind != "postgres" ||
		string(importer.got.ConnectionString) != "postgres://reader:password@host/finance" {
		t.Fatalf("handler request = %#v", importer.got)
	}
}

func TestUnixClientSendsExpectedRequest(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath string
	var got struct {
		DatabaseID       connection.ID   `json:"databaseId"`
		Kind             connection.Kind `json:"kind"`
		ConnectionString []byte          `json:"connectionString"`
	}
	path := startSocketHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"added","databaseId":"finance","connectionTested":false}`))
	}))
	client, err := newUnixClient(path)
	if err != nil {
		t.Fatalf("newUnixClient() error = %v", err)
	}
	_, err = client.Import(context.Background(), connection.ImportRequest{
		ID:               "finance",
		Kind:             "postgres",
		ConnectionString: []byte("private"),
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/connections/import" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if got.DatabaseID != "finance" || got.Kind != "postgres" || !bytes.Equal(got.ConnectionString, []byte("private")) {
		t.Fatalf("decoded request = %#v", got)
	}
}

func TestUnixClientSanitizesErrorResponse(t *testing.T) {
	t.Parallel()

	canary := "postgres://reader:password@host/finance"
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			path := startSocketHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"code":"invalid_connection_string","message":"` + canary + `"}`))
			}))
			client, err := newUnixClient(path)
			if err != nil {
				t.Fatalf("newUnixClient() error = %v", err)
			}
			_, err = client.Import(context.Background(), connection.ImportRequest{
				ID:               "finance",
				Kind:             "postgres",
				ConnectionString: []byte(canary),
			})
			if err == nil || strings.Contains(err.Error(), canary) {
				t.Fatalf("Import() error = %v, want safe error", err)
			}
		})
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("unix", path, 20*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("admin socket %q did not become available", path)
}

func startSocketHTTPServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve() error = %v", err)
		}
	})
	return path
}

type socketImporter struct {
	result connection.ImportResult
	got    connection.ImportRequest
}

func (i *socketImporter) Import(_ context.Context, request connection.ImportRequest) (connection.ImportResult, error) {
	i.got = connection.ImportRequest{
		ID:               request.ID,
		Kind:             request.Kind,
		ConnectionString: append([]byte(nil), request.ConnectionString...),
	}
	return i.result, nil
}
