package local

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testCredential = strings.Repeat("A", 43)

func TestStorePublishReadAndDelete(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	path := filepath.Join(root, "mcp-control-token")
	store, err := New(path)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := store.Publish(testCredential); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	got, err := store.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got != testCredential {
		t.Fatalf("Read() = %q, want %q", got, testCredential)
	}

	if err := store.Publish(strings.Repeat("A", 43)); err != nil {
		t.Fatalf("Publish(replacement) error = %v", err)
	}
	got, err = store.Read()
	if err != nil {
		t.Fatalf("Read(replacement) error = %v", err)
	}
	if got != strings.Repeat("A", 43) {
		t.Fatalf("Read(replacement) = %q, want replacement", got)
	}

	sibling := filepath.Join(root, "sibling")
	if err := os.WriteFile(sibling, []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile(sibling) error = %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(deleted path) error = %v, want not exist", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("Stat(sibling) error = %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete(missing) error = %v, want nil", err)
	}
}

func TestStoreRejectsUnsafeFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{name: "symlink", setup: func(t *testing.T, path string) {
			t.Helper()
			target := filepath.Join(filepath.Dir(path), "target")
			writeCredentialFile(t, target, testCredential, 0o600)
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
		{name: "wrong mode", setup: func(t *testing.T, path string) {
			t.Helper()
			writeCredentialFile(t, path, testCredential, 0o640)
		}},
		{name: "too large", setup: func(t *testing.T, path string) {
			t.Helper()
			writeCredentialFile(t, path, strings.Repeat("A", 1025), 0o600)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatalf("Chmod() error = %v", err)
			}
			path := filepath.Join(root, "mcp-control-token")
			store, err := New(path)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			test.setup(t, path)
			if _, err := store.Read(); err == nil {
				t.Fatal("Read() error = nil, want unsafe-file error")
			}
		})
	}
}

func TestStoreRejectsUnsafeParent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	store, err := New(filepath.Join(root, "mcp-control-token"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := store.Publish(testCredential); err == nil {
		t.Fatal("Publish() error = nil, want unsafe-parent error")
	}
}

func writeCredentialFile(t *testing.T, path, value string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
}
