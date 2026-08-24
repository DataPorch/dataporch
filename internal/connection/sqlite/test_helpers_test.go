package sqlite

import (
	"path/filepath"
	"testing"
)

func sqliteTestTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("resolve test directory %q: %v", directory, err)
	}
	return resolved
}
