package filestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/secret"
)

func TestStoreUpsertPreservesOtherDefinitions(t *testing.T) {
	t.Parallel()

	store, _ := newStore(t)
	if err := store.Upsert(t.Context(), definition("a", "local://secret-a")); err != nil {
		t.Fatalf("Upsert(a) error = %v", err)
	}

	if err := store.Upsert(t.Context(), definition("b", "local://secret-b")); err != nil {
		t.Fatalf("Upsert(b) error = %v", err)
	}

	definitions, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(definitions) != 2 {
		t.Fatalf("List() = %#v, want sorted a,b", definitions)
	}

	if definitions[0].ID != "a" || definitions[1].ID != "b" {
		t.Fatalf("List() = %#v, want definitions a and b", definitions)
	}
}

func TestStoreReplacementSurvivesReopen(t *testing.T) {
	t.Parallel()

	store, path := newStore(t)
	if err := store.Upsert(t.Context(), definition("finance", "local://secret-a")); err != nil {
		t.Fatalf("Upsert(first) error = %v", err)
	}

	updated := definition("finance", "local://secret-b")

	updated.Settings["host"] = "new.internal"
	if err := store.Upsert(t.Context(), updated); err != nil {
		t.Fatalf("Upsert(updated) error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	got, err := reopened.Lookup(context.Background(), "finance")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}

	if got.Settings["host"] != "new.internal" || got.SecretRefs["password"] != "local://secret-b" {
		t.Fatalf("Lookup() = %#v, want updated definition", got)
	}
}

func TestStoreRejectsDuplicateIDsOnLoad(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "connections.store")

	data := []byte(
		`{"connections":[{"id":"finance","kind":"postgres","settings":{},"secretRefs":{}},` +
			`{"id":"finance","kind":"postgres","settings":{},"secretRefs":{}}]}`,
	)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := Open(path); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Open() error = %v, want ErrStoreCorrupt", err)
	}
}

func TestStoreFailedWritePreservesCurrentSnapshot(t *testing.T) {
	t.Parallel()

	store, path := newStore(t)

	first := definition("finance", "local://secret-a")
	if err := store.Upsert(t.Context(), first); err != nil {
		t.Fatalf("Upsert(first) error = %v", err)
	}

	wantErr := errors.New("injected write failure")

	store.writeSnapshot = func(string, []connection.Definition) error { return wantErr }
	if err := store.Upsert(t.Context(), definition("finance", "local://secret-b")); !errors.Is(err, wantErr) {
		t.Fatalf("Upsert() error = %v, want %v", err, wantErr)
	}

	got, err := store.Lookup(t.Context(), "finance")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}

	if got.SecretRefs["password"] != "local://secret-a" {
		t.Fatalf("in-memory secret ref = %q, want local://secret-a", got.SecretRefs["password"])
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	got, err = reopened.Lookup(t.Context(), "finance")
	if err != nil {
		t.Fatalf("reopened Lookup() error = %v", err)
	}

	if got.SecretRefs["password"] != "local://secret-a" {
		t.Fatalf("persisted secret ref = %q, want local://secret-a", got.SecretRefs["password"])
	}
}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "connections.store")

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	return store, path
}

func definition(id connection.ID, ref secret.Reference) connection.Definition {
	return connection.Definition{
		ID:         id,
		Kind:       "postgres",
		Settings:   map[string]string{"host": "postgres.internal"},
		SecretRefs: map[string]secret.Reference{"password": ref},
	}
}
