package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/secret/local"
)

func TestInitializeSecretsUsesConfiguredPaths(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	cfg := config.Config{
		MasterKeyPath:    filepath.Join(base, "key", "master.key"),
		SecretsStorePath: filepath.Join(base, "store", "secrets.store"),
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
