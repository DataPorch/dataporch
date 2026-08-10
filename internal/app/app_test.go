package app

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/config"
)

func TestNew(t *testing.T) {
	t.Parallel()

	if _, err := New(testConfig(), nil); err == nil {
		t.Fatal("New() error = nil, want logger validation error")
	}
}

func TestApp_Run(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	application, err := New(testConfig(), logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := application.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func testConfig() config.Config {
	return config.Config{
		HTTPAddress:          "127.0.0.1:0",
		ResourceLimit:        10,
		ShutdownPeriod:       time.Second,
		AdminSocketPath:      "/tmp/dataporch/admin.sock",
		MasterKeyPath:        "/tmp/dataporch/master.key",
		SecretsStorePath:     "/tmp/dataporch/secrets.store",
		ConnectionsStorePath: "/tmp/dataporch/connections.store",
	}
}
