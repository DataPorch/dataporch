package local

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestInitCreatesKeyAndEmptyStore(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	if err := Init(paths); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	key, err := os.ReadFile(paths.KeyPath)
	if err != nil {
		t.Fatalf("ReadFile(key) error = %v", err)
	}

	if len(key) != masterKeySize {
		t.Errorf("master key length = %d, want %d", len(key), masterKeySize)
	}

	assertMode(t, paths.KeyPath, 0o600)
	assertMode(t, paths.StorePath, 0o600)

	store, err := os.ReadFile(paths.StorePath)
	if err != nil {
		t.Fatalf("ReadFile(store) error = %v", err)
	}

	if string(store) != `{"entries":{}}` {
		t.Fatalf("store JSON = %s, want {\"entries\":{}}", store)
	}
}

func TestInitRefusesExistingKey(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	if err := os.MkdirAll(filepath.Dir(paths.KeyPath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := os.WriteFile(paths.KeyPath, []byte("existing"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := Init(paths); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("Init() error = %v, want ErrAlreadyInitialized", err)
	}
}

func TestInitRefusesExistingStore(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	if err := os.MkdirAll(filepath.Dir(paths.StorePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := os.WriteFile(paths.StorePath, []byte(`{"entries":{}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := Init(paths); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("Init() error = %v, want ErrAlreadyInitialized", err)
	}

	if _, err := os.Stat(paths.KeyPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(key) error = %v, want not exist", err)
	}
}

func TestInitRollsBackNewKeyWhenStoreCreationFails(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	wantErr := errors.New("store creation failed")

	var createCalls int

	create := func(path string, data []byte, permission fs.FileMode) error {
		createCalls++
		if createCalls == 2 {
			return wantErr
		}

		return os.WriteFile(path, data, permission)
	}

	err := initStore(paths, bytes.NewReader(make([]byte, masterKeySize)), create)
	if !errors.Is(err, wantErr) {
		t.Fatalf("initStore() error = %v, want %v", err, wantErr)
	}

	if _, err := os.Stat(paths.KeyPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(key) error = %v, want not exist", err)
	}
}

func testPaths(t *testing.T) Paths {
	t.Helper()

	base := t.TempDir()

	return Paths{
		KeyPath:   filepath.Join(base, "key", "master.key"),
		StorePath: filepath.Join(base, "store", "secrets.store"),
	}
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}

	if got := info.Mode().Perm(); got != want {
		t.Errorf(
			"permissions for %s = %o, want %o",
			path,
			got,
			want,
		)
	}
}
