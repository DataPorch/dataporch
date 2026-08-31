package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DataPorch/dataporch/internal/config"
	"github.com/DataPorch/dataporch/internal/secret/local"
)

func TestInitializeSecretsUsesConfiguredPaths(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) error = %v", base, err)
	}
	cfg := config.Config{
		MasterKeyPath:    filepath.Join(resolvedBase, "key", "master.key"),
		SecretsStorePath: filepath.Join(resolvedBase, "store", "secrets.store"),
	}

	if err := InitializeSecrets(cfg); err != nil {
		t.Fatalf("InitializeSecrets() error = %v", err)
	}

	if _, err := os.Stat(cfg.MasterKeyPath); err != nil {
		t.Fatalf("Stat(master key) error = %v", err)
	}

	if _, err := os.Stat(cfg.SecretsStorePath); err != nil {
		t.Fatalf("Stat(secret store) error = %v", err)
	}

	if err := InitializeSecrets(cfg); !errors.Is(err, local.ErrAlreadyInitialized) {
		t.Fatalf("InitializeSecrets() error = %v, want ErrAlreadyInitialized", err)
	}
}
