//go:build integration

package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
)

func TestIntegrationSQLiteFirstUseFileVerification(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	validPath := filepath.Join(root, "valid.db")
	createIntegrationDatabase(t, validPath, []string{
		`CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO items(value) VALUES ('valid')`,
	})

	missingPath := filepath.Join(root, "missing.db")
	emptyPath := filepath.Join(root, "empty.db")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatalf("WriteFile(empty) error = %v", err)
	}
	directoryPath := filepath.Join(root, "directory.db")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatalf("Mkdir(directory) error = %v", err)
	}
	symlinkPath := filepath.Join(root, "symlink.db")
	if err := os.Symlink(validPath, symlinkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	malformedPath := filepath.Join(root, "malformed.db")
	if err := os.WriteFile(malformedPath, []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("WriteFile(malformed) error = %v", err)
	}
	corruptPath := filepath.Join(root, "corrupt.db")
	if err := os.WriteFile(corruptPath, []byte("SQLite format 3\x00corrupt"), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt) error = %v", err)
	}
	permissionPath := filepath.Join(root, "permission.db")
	createIntegrationDatabase(t, permissionPath, []string{
		`CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT)`,
	})
	if err := os.Chmod(permissionPath, 0); err != nil {
		t.Fatalf("Chmod(permission) error = %v", err)
	}

	for _, test := range []struct {
		name          string
		path          string
		want          bool
		permission    bool
		restoreOnExit bool
	}{
		{name: "missing", path: missingPath},
		{name: "empty", path: emptyPath},
		{name: "directory", path: directoryPath},
		{name: "final symlink", path: symlinkPath},
		{name: "malformed", path: malformedPath},
		{name: "corrupt", path: corruptPath},
		{name: "permission denied", path: permissionPath, permission: true, restoreOnExit: true},
		{name: "valid", path: validPath, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.restoreOnExit {
				defer func() { _ = os.Chmod(test.path, 0o600) }()
			}
			before := integrationSnapshot(t, root)
			runtime, err := NewRuntime(&fixturePreparer{path: test.path})
			if err != nil {
				t.Fatalf("NewRuntime() error = %v", err)
			}
			client, err := runtime.open(t.Context(), "fixture", accessModeQuery)
			if test.permission && err == nil {
				if client != nil {
					_ = client.close()
				}
				_ = runtime.Close(t.Context())
				t.Skip("permission bits are bypassed by the test process")
			}
			if test.want {
				if err != nil {
					t.Fatalf("Runtime.open(valid) error = %v", err)
				}
				if client == nil {
					t.Fatal("Runtime.open(valid) client = nil")
				}
				if err := client.close(); err != nil {
					t.Fatalf("client.close() error = %v", err)
				}
			} else if err == nil {
				if client != nil {
					_ = client.close()
				}
				t.Fatal("Runtime.open() error = nil, want sanitized failure")
			} else {
				sanitized := projectSQLiteError(t.Context(), t.Context(), err, sqliteErrorPhaseOpen)
				if filepath.Base(test.path) != "" && stringsContainsPath(sanitized.Error(), test.path) {
					t.Fatalf("Runtime.open() leaked path: %v", sanitized)
				}
			}
			if err := runtime.Close(t.Context()); err != nil {
				t.Fatalf("Runtime.Close() error = %v", err)
			}
			after := integrationSnapshot(t, root)
			assertIntegrationSnapshotUnchanged(t, before, after, nil)
		})
	}
}

func TestIntegrationSQLiteLiveUpdateAndAtomicReplacement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "live.db")
	createIntegrationDatabase(t, path, []string{
		`CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO items(value) VALUES ('old')`,
	})
	runtime, err := NewRuntime(&fixturePreparer{path: path})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	readValue := func() string {
		t.Helper()
		client, openErr := runtime.open(t.Context(), "fixture", accessModeQuery)
		if openErr != nil {
			t.Fatalf("Runtime.open() error = %v", openErr)
		}
		stmt, tail, prepareErr := client.conn.Prepare(`SELECT value FROM items ORDER BY id`)
		if prepareErr != nil || stmt == nil || tail != "" {
			_ = client.close()
			t.Fatalf("Prepare(read) = stmt %#v tail %q err %v", stmt, tail, prepareErr)
		}
		defer stmt.Close()
		if !stmt.Step() {
			_ = client.close()
			t.Fatalf("Step(read) = false, err %v", stmt.Err())
		}
		value := stmt.ColumnText(0)
		if closeErr := stmt.Close(); closeErr != nil {
			t.Fatalf("statement close error = %v", closeErr)
		}
		if closeErr := client.close(); closeErr != nil {
			t.Fatalf("client.close() error = %v", closeErr)
		}
		return value
	}
	if got := readValue(); got != "old" {
		t.Fatalf("initial value = %q, want old", got)
	}

	writer := openIntegrationWritable(t, path)
	if err := writer.Exec(`UPDATE items SET value = 'updated' WHERE id = 1`); err != nil {
		t.Fatalf("writer update error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer close error = %v", err)
	}
	if got := readValue(); got != "updated" {
		t.Fatalf("updated value = %q, want updated", got)
	}

	replacement := filepath.Join(root, "replacement.db")
	createIntegrationDatabase(t, replacement, []string{
		`CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO items(value) VALUES ('replacement')`,
	})
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("Rename(replacement) error = %v", err)
	}
	if got := readValue(); got != "replacement" {
		t.Fatalf("replacement value = %q, want replacement", got)
	}
}

func TestIntegrationSQLiteWALDoesNotMutateSidecars(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "wal.db")
	writer := openIntegrationWritable(t, path)
	if err := writer.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = writer.Close()
		t.Fatalf("enable WAL error = %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO items(value) VALUES ('wal-value')`,
	} {
		if err := writer.Exec(statement); err != nil {
			_ = writer.Close()
			t.Fatalf("writer Exec(%q) error = %v", statement, err)
		}
	}
	walPath := path + "-wal"
	shmPath := path + "-shm"
	if _, err := os.Stat(walPath); err != nil {
		_ = writer.Close()
		t.Fatalf("WAL sidecar missing: %v", err)
	}
	if _, err := os.Stat(shmPath); err != nil {
		_ = writer.Close()
		t.Fatalf("SHM sidecar missing: %v", err)
	}
	before := integrationSnapshot(t, root)

	runtime, err := NewRuntime(&fixturePreparer{path: path})
	if err != nil {
		_ = writer.Close()
		t.Fatalf("NewRuntime() error = %v", err)
	}
	client, err := runtime.open(t.Context(), "fixture", accessModeQuery)
	if err != nil {
		_ = writer.Close()
		_ = runtime.Close(context.Background())
		t.Fatalf("Runtime.open(WAL) error = %v", err)
	}
	stmt, tail, err := client.conn.Prepare(`SELECT value FROM items`)
	if err != nil || stmt == nil || tail != "" {
		_ = writer.Close()
		_ = client.close()
		_ = runtime.Close(context.Background())
		t.Fatalf("Prepare(WAL) = stmt %#v tail %q err %v", stmt, tail, err)
	}
	if !stmt.Step() || stmt.ColumnText(0) != "wal-value" {
		_ = stmt.Close()
		_ = writer.Close()
		_ = client.close()
		_ = runtime.Close(context.Background())
		t.Fatalf("WAL read did not return committed value")
	}
	_ = stmt.Close()
	if err := client.close(); err != nil {
		t.Fatalf("client.close(WAL) error = %v", err)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("Runtime.Close(WAL) error = %v", err)
	}
	after := integrationSnapshot(t, root)
	// SQLite updates its WAL-index lock/read-mark bookkeeping in -shm while
	// reading a live WAL. The database and WAL payload remain unchanged.
	assertIntegrationSnapshotUnchanged(t, before, after, func(name string) bool {
		return name == filepath.Base(shmPath)
	})
	if err := writer.Close(); err != nil {
		t.Fatalf("writer close error = %v", err)
	}
}

func TestIntegrationSQLiteConcurrentReadsInvalidateAndShutdown(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "concurrent.db")
	createIntegrationDatabase(t, path, []string{
		`CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO items(value) VALUES ('value')`,
	})
	runtime, err := NewRuntime(&fixturePreparer{path: path})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	identities := make(chan uintptr, 50)
	var group sync.WaitGroup
	for index := 0; index < 50; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			client, openErr := runtime.open(t.Context(), "fixture", accessModeQuery)
			if openErr != nil {
				t.Errorf("Runtime.open() error = %v", openErr)
				return
			}
			value := reflect.ValueOf(client.conn)
			if value.Kind() == reflect.Pointer {
				identities <- value.Pointer()
			}
			if closeErr := client.close(); closeErr != nil {
				t.Errorf("client.close() error = %v", closeErr)
			}
		}()
	}
	group.Wait()
	close(identities)
	unique := make(map[uintptr]struct{})
	for identity := range identities {
		unique[identity] = struct{}{}
	}
	if len(unique) < 2 {
		t.Fatalf("physical connection identities = %#v, want independent connections", unique)
	}

	active, err := runtime.open(t.Context(), "fixture", accessModeQuery)
	if err != nil {
		t.Fatalf("Runtime.open(active) error = %v", err)
	}
	runtime.Invalidate("fixture")
	newClient, err := runtime.open(t.Context(), "fixture", accessModeQuery)
	if err != nil {
		_ = active.close()
		t.Fatalf("Runtime.open(after invalidation) error = %v", err)
	}
	if err := newClient.close(); err != nil {
		t.Fatalf("new client close error = %v", err)
	}

	shortContext, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if err := runtime.Close(shortContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Runtime.Close(expired) error = %v, want deadline", err)
	}
	if err := active.close(); err != nil {
		t.Fatalf("active client close error = %v", err)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("Runtime.Close(final) error = %v", err)
	}
}

type integrationFileState struct {
	mode        os.FileMode
	size        int64
	modTime     time.Time
	contentHash [sha256.Size]byte
	hasContent  bool
}

func integrationSnapshot(t *testing.T, directory string) map[string]integrationFileState {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", directory, err)
	}
	snapshot := make(map[string]integrationFileState, len(entries))
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr != nil {
			t.Fatalf("Info(%q) error = %v", entry.Name(), statErr)
		}
		state := integrationFileState{mode: info.Mode(), size: info.Size(), modTime: info.ModTime()}
		if info.Mode().IsRegular() {
			content, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
			if readErr == nil {
				state.contentHash = sha256.Sum256(content)
				state.hasContent = true
			} else if !errors.Is(readErr, fs.ErrPermission) {
				t.Fatalf("ReadFile(%q) error = %v", entry.Name(), readErr)
			}
		}
		snapshot[entry.Name()] = state
	}
	return snapshot
}

func assertIntegrationSnapshotUnchanged(
	t *testing.T,
	before, after map[string]integrationFileState,
	allowModTimeChange func(string) bool,
) {
	t.Helper()
	if !reflect.DeepEqual(keysOfIntegrationSnapshot(before), keysOfIntegrationSnapshot(after)) {
		t.Fatalf("filesystem entries changed: before=%#v after=%#v", before, after)
	}
	for name, beforeState := range before {
		afterState := after[name]
		if beforeState.mode != afterState.mode || beforeState.size != afterState.size {
			t.Fatalf("filesystem metadata changed for %q: before=%#v after=%#v", name, beforeState, afterState)
		}
		if !beforeState.modTime.Equal(afterState.modTime) && (allowModTimeChange == nil || !allowModTimeChange(name)) {
			t.Fatalf("filesystem modification time changed for %q: before=%v after=%v", name, beforeState.modTime, afterState.modTime)
		}
		if !beforeState.hasContent || !afterState.hasContent {
			continue
		}
		if beforeState.contentHash != afterState.contentHash && (allowModTimeChange == nil || !allowModTimeChange(name)) {
			t.Fatalf("filesystem content changed for %q", name)
		}
	}
}

func keysOfIntegrationSnapshot(snapshot map[string]integrationFileState) []string {
	keys := make([]string, 0, len(snapshot))
	for key := range snapshot {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func createIntegrationDatabase(t *testing.T, path string, statements []string) {
	t.Helper()
	conn, err := sqlite3.OpenFlags(path, sqlite3.OPEN_READWRITE|sqlite3.OPEN_CREATE|sqlite3.OPEN_URI)
	if err != nil {
		t.Fatalf("OpenFlags(%q) error = %v", path, err)
	}
	for _, statement := range statements {
		if err := conn.Exec(statement); err != nil {
			_ = conn.Close()
			t.Fatalf("fixture Exec(%q) error = %v", statement, err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("fixture close error = %v", err)
	}
}

func openIntegrationWritable(t *testing.T, path string) *sqlite3.Conn {
	t.Helper()
	conn, err := sqlite3.OpenFlags(path, sqlite3.OPEN_READWRITE|sqlite3.OPEN_CREATE|sqlite3.OPEN_URI)
	if err != nil {
		t.Fatalf("OpenFlags(writable %q) error = %v", path, err)
	}
	return conn
}

func stringsContainsPath(value, path string) bool {
	return path != "" && strings.Contains(value, path)
}
