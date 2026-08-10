package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateRefusesExistingPath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := Create(path, []byte("new"), 0o600); err == nil {
		t.Fatal("Create() error = nil, want non-nil")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "existing" {
		t.Fatalf("file contents = %q, want %q", data, "existing")
	}
}

func TestCreateUsesRestrictivePermissions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "secret")
	if err := Create(path, []byte("value"), 0o600); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("permissions = %o, want 600", got)
	}
}

func TestReplacePublishesCompleteContent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "snapshot")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := Replace(path, []byte("complete new snapshot"), 0o600); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "complete new snapshot" {
		t.Fatalf("file contents = %q, want complete new snapshot", data)
	}
}

func TestReplacePreservesOldFileWhenTemporaryWriteFails(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "snapshot")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	wantErr := errors.New("temporary write failed")
	if err := replace(path, []byte("new"), 0o600, failingTemporaryFactory(wantErr)); !errors.Is(err, wantErr) {
		t.Fatalf("replace() error = %v, want %v", err, wantErr)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "old" {
		t.Fatalf("file contents = %q, want %q", data, "old")
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "snapshot" {
		t.Fatalf("directory entries = %#v, want only snapshot", entries)
	}
}

type failingTemporaryFile struct {
	*os.File
	err error
}

func (f failingTemporaryFile) Write([]byte) (int, error) {
	return 0, f.err
}

func failingTemporaryFactory(wantErr error) createTemporaryFile {
	return func(directory, pattern string) (temporaryFile, error) {
		file, err := os.CreateTemp(directory, pattern)
		if err != nil {
			return nil, err
		}

		return failingTemporaryFile{File: file, err: wantErr}, nil
	}
}
