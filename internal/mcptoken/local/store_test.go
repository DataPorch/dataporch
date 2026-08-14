package local

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/mcptoken"
)

func TestNew(t *testing.T) {
	t.Parallel()

	if _, err := New(" "); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("New(empty path) error = %v, want ErrInvalidPath", err)
	}

	store, err := New("/tmp/mcp-token.json")
	if err != nil {
		t.Fatalf("New(valid path) error = %v", err)
	}

	if store == nil {
		t.Fatal("New(valid path) returned nil store")
	}
}

func TestStore_LoadMissing(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	got, exists, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if exists {
		t.Fatal("Load() exists = true, want false")
	}

	if got != (mcptoken.PersistedState{}) {
		t.Fatalf("Load() state = %#v, want zero state", got)
	}
}

func TestStore_SaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mcp-token.json")

	store, err := New(path)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	state := validState()

	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if got := info.Mode().Perm(); got != filePermission {
		t.Fatalf("file mode = %o, want %o", got, filePermission)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if strings.Contains(string(data), "fixture-token") {
		t.Fatal("store file contains plaintext token material")
	}

	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	for _, field := range []string{"version", "verifier", "created_at", "rotated_at"} {
		if _, ok := document[field]; !ok {
			t.Fatalf("persisted document missing %q: %s", field, data)
		}
	}

	if got := document["rotated_at"]; got == nil {
		t.Fatal("persisted rotated_at is nil, want timestamp for valid rotated state")
	}

	got, exists, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !exists {
		t.Fatal("Load() exists = false, want true")
	}

	assertPersistedState(t, got, state)
}

func TestStore_SaveWritesNullRotation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mcp-token.json")

	store, err := New(path)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	state := validState()

	state.RotatedAt = nil
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !strings.Contains(string(data), `"rotated_at":null`) {
		t.Fatalf("persisted document = %s, want null rotated_at", data)
	}
}

func TestStore_LoadRejectsInvalidFiles(t *testing.T) {
	t.Parallel()

	state := validState()
	validVerifier := base64.RawURLEncoding.EncodeToString(state.Verifier[:])
	validCreated := state.CreatedAt.Format(time.RFC3339Nano)
	validRotated := state.RotatedAt.Format(time.RFC3339Nano)

	tests := []struct {
		name string
		data string
	}{
		{
			name: "malformed json",
			data: `{"version":1`,
		},
		{
			name: "unsupported version",
			data: `{"version":2,"verifier":"` + validVerifier + `","created_at":"` + validCreated + `","rotated_at":null}`,
		},
		{
			name: "invalid verifier encoding",
			data: `{"version":1,"verifier":"!","created_at":"` + validCreated + `","rotated_at":null}`,
		},
		{
			name: "invalid verifier length",
			data: `{"version":1,"verifier":"AQ","created_at":"` + validCreated + `","rotated_at":null}`,
		},
		{
			name: "padded verifier",
			data: `{"version":1,"verifier":"` + validVerifier + `=","created_at":"` + validCreated + `","rotated_at":null}`,
		},
		{
			name: "invalid created timestamp",
			data: `{"version":1,"verifier":"` + validVerifier + `","created_at":"not-a-time","rotated_at":null}`,
		},
		{
			name: "non-utc created timestamp",
			data: `{"version":1,"verifier":"` + validVerifier + `","created_at":"2026-08-14T10:00:00+08:00","rotated_at":null}`,
		},
		{
			name: "invalid rotated timestamp",
			data: `{"version":1,"verifier":"` + validVerifier + `","created_at":"` + validCreated + `","rotated_at":"not-a-time"}`,
		},
		{
			name: "rotation before creation",
			data: `{"version":1,"verifier":"` + validVerifier + `","created_at":"` + validRotated + `","rotated_at":"` + validCreated + `"}`,
		},
		{
			name: "unknown field",
			data: `{"version":1,"verifier":"` + validVerifier + `","created_at":"` + validCreated + `","rotated_at":null,"token":"dp-secret"}`,
		},
		{
			name: "trailing json",
			data: `{"version":1,"verifier":"` + validVerifier + `","created_at":"` + validCreated + `","rotated_at":null}{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newTestStore(t)
			writeStoreFile(t, store.path, []byte(tt.data), filePermission)

			_, exists, err := store.Load(context.Background())
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("Load() error = %v, want ErrStoreCorrupt", err)
			}

			if exists {
				t.Fatal("Load() exists = true for corrupt file")
			}
		})
	}
}

func TestStore_LoadRejectsUnsafeFiles(t *testing.T) {
	t.Parallel()

	state := validState()

	data, err := encode(state)
	if err != nil {
		t.Fatalf("encode() error = %v", err)
	}

	tests := []struct {
		name      string
		setup     func(t *testing.T, path string)
		wantError error
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "target.json")
				writeStoreFile(t, target, data, filePermission)

				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
			},
			wantError: ErrStoreCorrupt,
		},
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				t.Helper()

				if err := os.Mkdir(path, filePermission); err != nil {
					t.Fatalf("Mkdir() error = %v", err)
				}
			},
			wantError: ErrStoreCorrupt,
		},
		{
			name: "group permission",
			setup: func(t *testing.T, path string) {
				t.Helper()
				writeStoreFile(t, path, data, 0o640)
			},
			wantError: ErrInvalidPermissions,
		},
		{
			name: "world permission",
			setup: func(t *testing.T, path string) {
				t.Helper()
				writeStoreFile(t, path, data, 0o604)
			},
			wantError: ErrInvalidPermissions,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "mcp-token.json")

			store, err := New(path)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			tt.setup(t, path)

			_, _, err = store.Load(context.Background())
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("Load() error = %v, want %v", err, tt.wantError)
			}
		})
	}
}

func TestStore_SaveRejectsInvalidState(t *testing.T) {
	t.Parallel()

	createdAt := validState().CreatedAt
	tests := []struct {
		name  string
		state mcptoken.PersistedState
	}{
		{
			name: "zero verifier",
			state: mcptoken.PersistedState{
				CreatedAt: createdAt,
			},
		},
		{
			name: "zero creation timestamp",
			state: mcptoken.PersistedState{
				Verifier: validState().Verifier,
			},
		},
		{
			name: "rotation before creation",
			state: func() mcptoken.PersistedState {
				state := validState()
				rotatedAt := state.CreatedAt.Add(-time.Second)
				state.RotatedAt = &rotatedAt

				return state
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newTestStore(t)
			if err := store.Save(context.Background(), tt.state); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("Save() error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

func TestStore_SaveFailurePreservesExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "missing", "mcp-token.json")

	store, err := New(path)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := store.Save(context.Background(), validState()); err == nil {
		t.Fatal("Save() error = nil, want failure for missing parent")
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(saved path) error = %v, want not exist", err)
	}
}

func TestStore_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
		check func(t *testing.T, path string)
	}{
		{
			name:  "missing file is idempotent",
			setup: func(*testing.T, string) {},
			check: func(t *testing.T, path string) {
				t.Helper()

				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Lstat() error = %v, want not exist", err)
				}
			},
		},
		{
			name: "regular file is removed",
			setup: func(t *testing.T, path string) {
				t.Helper()
				writeStoreFile(t, path, []byte("state"), filePermission)
			},
			check: func(t *testing.T, path string) {
				t.Helper()

				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Lstat() error = %v, want not exist", err)
				}
			},
		},
		{
			name: "symlink is unlinked without touching target",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "target")
				writeStoreFile(t, target, []byte("target"), filePermission)

				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
			},
			check: func(t *testing.T, path string) {
				t.Helper()

				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Lstat(link) error = %v, want not exist", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "mcp-token.json")

			store, err := New(path)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			tt.setup(t, path)

			if err := store.Delete(context.Background()); err != nil {
				t.Fatalf("Delete() error = %v", err)
			}

			if err := store.Delete(context.Background()); err != nil {
				t.Fatalf("second Delete() error = %v", err)
			}

			tt.check(t, path)
		})
	}
}

func TestStore_DeleteRefusesDirectories(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mcp-token.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	child := filepath.Join(path, "child")
	if err := os.WriteFile(child, []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := New(path)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := store.Delete(context.Background()); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Delete() error = %v, want ErrStoreCorrupt", err)
	}

	if _, err := os.Stat(child); err != nil {
		t.Fatalf("Stat(child) error = %v, want child preserved", err)
	}
}

func TestStore_SaveReplacesSymlinkWithoutFollowingTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-token.json")
	target := filepath.Join(dir, "target.json")
	writeStoreFile(t, target, []byte("target"), filePermission)

	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	store, err := New(path)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := store.Save(context.Background(), validState()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if got, err := os.ReadFile(target); err != nil || string(got) != "target" {
		t.Fatalf("target contents = %q, error = %v, want unchanged target", got, err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(path) error = %v", err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("Save() left symlink at configured path")
	}
}

func TestStore_ContextCancellation(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := store.Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(canceled) error = %v, want context.Canceled", err)
	}

	if err := store.Save(ctx, validState()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save(canceled) error = %v, want context.Canceled", err)
	}

	if err := store.Delete(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete(canceled) error = %v, want context.Canceled", err)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()

	store, err := New(filepath.Join(t.TempDir(), "mcp-token.json"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return store
}

func validState() mcptoken.PersistedState {
	createdAt := time.Date(2026, time.August, 14, 10, 0, 0, 0, time.UTC)
	rotatedAt := createdAt.Add(time.Hour)

	return mcptoken.PersistedState{
		Verifier:  sha256.Sum256([]byte("fixture-token")),
		CreatedAt: createdAt,
		RotatedAt: &rotatedAt,
	}
}

func writeStoreFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
}

func assertPersistedState(t *testing.T, got, want mcptoken.PersistedState) {
	t.Helper()

	if got.Verifier != want.Verifier {
		t.Fatalf("Verifier = %x, want %x", got.Verifier, want.Verifier)
	}

	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}

	if got.RotatedAt == nil || want.RotatedAt == nil {
		if got.RotatedAt != nil || want.RotatedAt != nil {
			t.Fatalf("RotatedAt = %v, want %v", got.RotatedAt, want.RotatedAt)
		}

		return
	}

	if !got.RotatedAt.Equal(*want.RotatedAt) {
		t.Fatalf("RotatedAt = %v, want %v", got.RotatedAt, want.RotatedAt)
	}
}
