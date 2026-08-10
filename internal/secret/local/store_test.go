package local

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/adamraziv/dataporch/internal/secret"
)

const canary = "dataporch-canary-password-7f4a"

func TestStoreResolveRoundTrip(t *testing.T) {
	t.Parallel()

	store, _ := initializedStore(t)

	ref, err := store.Store(t.Context(), []byte(canary))
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	plaintext, err := store.Resolve(t.Context(), ref)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if string(plaintext) != canary {
		t.Fatalf("Resolve() = %q, want %q", plaintext, canary)
	}
}

func TestStoreDoesNotPersistPlaintext(t *testing.T) {
	t.Parallel()

	store, paths := initializedStore(t)
	if _, err := store.Store(t.Context(), []byte(canary)); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	persisted, err := os.ReadFile(paths.StorePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if bytes.Contains(persisted, []byte(canary)) {
		t.Fatal("secret store contains plaintext canary")
	}
}

func TestStoreUsesDifferentCiphertextForSamePlaintext(t *testing.T) {
	t.Parallel()

	store, paths := initializedStore(t)

	first, err := store.Store(t.Context(), []byte(canary))
	if err != nil {
		t.Fatalf("Store(first) error = %v", err)
	}

	second, err := store.Store(t.Context(), []byte(canary))
	if err != nil {
		t.Fatalf("Store(second) error = %v", err)
	}

	entries := readEntries(t, paths)

	_, firstID, err := first.Parts()
	if err != nil {
		t.Fatalf("first.Parts() error = %v", err)
	}

	_, secondID, err := second.Parts()
	if err != nil {
		t.Fatalf("second.Parts() error = %v", err)
	}

	if bytes.Equal(entries[firstID], entries[secondID]) {
		t.Fatal("same plaintext produced identical ciphertext")
	}
}

func TestResolveRejectsTamperedCiphertext(t *testing.T) {
	t.Parallel()

	store, paths := initializedStore(t)

	ref, err := store.Store(t.Context(), []byte(canary))
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	entries := readEntries(t, paths)

	_, id, err := ref.Parts()
	if err != nil {
		t.Fatalf("Parts() error = %v", err)
	}

	entries[id][len(entries[id])-1] ^= 0xff
	writeEntries(t, paths, entries)

	store, err = Open(paths)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if _, err := store.Resolve(t.Context(), ref); err == nil {
		t.Fatal("Resolve() error = nil, want non-nil")
	}
}

func TestResolveRejectsWrongMasterKey(t *testing.T) {
	t.Parallel()

	store, paths := initializedStore(t)

	ref, err := store.Store(t.Context(), []byte(canary))
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	if err := os.WriteFile(paths.KeyPath, bytes.Repeat([]byte{1}, masterKeySize), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err = Open(paths)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if _, err := store.Resolve(t.Context(), ref); err == nil {
		t.Fatal("Resolve() error = nil, want non-nil")
	}
}

func TestResolveRejectsCiphertextMovedToAnotherID(t *testing.T) {
	t.Parallel()

	store, paths := initializedStore(t)

	first, err := store.Store(t.Context(), []byte("first"))
	if err != nil {
		t.Fatalf("Store(first) error = %v", err)
	}

	second, err := store.Store(t.Context(), []byte("second"))
	if err != nil {
		t.Fatalf("Store(second) error = %v", err)
	}

	entries := readEntries(t, paths)

	_, firstID, err := first.Parts()
	if err != nil {
		t.Fatalf("first.Parts() error = %v", err)
	}

	_, secondID, err := second.Parts()
	if err != nil {
		t.Fatalf("second.Parts() error = %v", err)
	}

	entries[secondID] = entries[firstID]
	writeEntries(t, paths, entries)

	store, err = Open(paths)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if _, err := store.Resolve(t.Context(), second); err == nil {
		t.Fatal("Resolve() error = nil, want non-nil")
	}
}

func TestOpenRejectsPermissiveKeyFile(t *testing.T) {
	t.Parallel()

	_, paths := initializedStore(t)
	if err := os.Chmod(paths.KeyPath, 0o640); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	if _, err := Open(paths); !errors.Is(err, ErrInvalidPermissions) {
		t.Fatalf("Open() error = %v, want ErrInvalidPermissions", err)
	}
}

func TestOpenRejectsPermissiveStoreFile(t *testing.T) {
	t.Parallel()

	_, paths := initializedStore(t)
	if err := os.Chmod(paths.StorePath, 0o604); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	if _, err := Open(paths); !errors.Is(err, ErrInvalidPermissions) {
		t.Fatalf("Open() error = %v, want ErrInvalidPermissions", err)
	}
}

func TestDeleteRemovesOnlyRequestedSecret(t *testing.T) {
	t.Parallel()

	store, _ := initializedStore(t)

	first, err := store.Store(t.Context(), []byte("first"))
	if err != nil {
		t.Fatalf("Store(first) error = %v", err)
	}

	second, err := store.Store(t.Context(), []byte("second"))
	if err != nil {
		t.Fatalf("Store(second) error = %v", err)
	}

	if err := store.Delete(t.Context(), first); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := store.Resolve(t.Context(), first); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Resolve(first) error = %v, want ErrSecretNotFound", err)
	}

	plaintext, err := store.Resolve(t.Context(), second)
	if err != nil {
		t.Fatalf("Resolve(second) error = %v", err)
	}

	if string(plaintext) != "second" {
		t.Fatalf("Resolve(second) = %q, want second", plaintext)
	}
}

func TestDeleteMissingSecret(t *testing.T) {
	t.Parallel()

	store, _ := initializedStore(t)
	if err := store.Delete(t.Context(), "local://missing"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Delete() error = %v, want ErrSecretNotFound", err)
	}
}

func TestFailedStorePreservesDiskAndMemorySnapshot(t *testing.T) {
	t.Parallel()

	store, paths := initializedStore(t)

	ref, err := store.Store(t.Context(), []byte("existing"))
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	wantErr := errors.New("injected write failure")

	store.writeSnapshot = func(string, map[string][]byte) error { return wantErr }
	if _, err := store.Store(t.Context(), []byte("new")); !errors.Is(err, wantErr) {
		t.Fatalf("Store() error = %v, want %v", err, wantErr)
	}

	assertSecretResolves(
		t,
		store,
		ref,
		"existing",
	)

	reopened, err := Open(paths)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	assertSecretResolves(
		t,
		reopened,
		ref,
		"existing",
	)
}

func TestFailedDeletePreservesDiskAndMemorySnapshot(t *testing.T) {
	t.Parallel()

	store, paths := initializedStore(t)

	ref, err := store.Store(t.Context(), []byte("existing"))
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	wantErr := errors.New("injected write failure")

	store.writeSnapshot = func(string, map[string][]byte) error { return wantErr }
	if err := store.Delete(t.Context(), ref); !errors.Is(err, wantErr) {
		t.Fatalf("Delete() error = %v, want %v", err, wantErr)
	}

	assertSecretResolves(
		t,
		store,
		ref,
		"existing",
	)

	reopened, err := Open(paths)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	assertSecretResolves(
		t,
		reopened,
		ref,
		"existing",
	)
}

func TestConcurrentStoreResolveAndDelete(t *testing.T) {
	t.Parallel()

	store, _ := initializedStore(t)

	const workers = 8

	errorsByWorker := make(chan error, workers)

	var group sync.WaitGroup
	for worker := range workers {
		group.Go(func() {
			value := fmt.Sprintf("value-%d", worker)

			ref, err := store.Store(t.Context(), []byte(value))
			if err != nil {
				errorsByWorker <- fmt.Errorf("storing: %w", err)
				return
			}

			plaintext, err := store.Resolve(t.Context(), ref)
			if err != nil {
				errorsByWorker <- fmt.Errorf("resolving: %w", err)
				return
			}

			if string(plaintext) != value {
				errorsByWorker <- fmt.Errorf("resolved %q, want %q", plaintext, value)
				return
			}

			if err := store.Delete(t.Context(), ref); err != nil {
				errorsByWorker <- fmt.Errorf("deleting: %w", err)
			}
		})
	}

	group.Wait()
	close(errorsByWorker)

	for err := range errorsByWorker {
		t.Error(err)
	}
}

func initializedStore(t *testing.T) (*Store, Paths) {
	t.Helper()

	paths := testPaths(t)
	if err := Init(paths); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	store, err := Open(paths)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	return store, paths
}

func readEntries(t *testing.T, paths Paths) map[string][]byte {
	t.Helper()

	data, err := os.ReadFile(paths.StorePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var snapshot emptySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	return snapshot.Entries
}

func writeEntries(t *testing.T, paths Paths, entries map[string][]byte) {
	t.Helper()

	data, err := json.Marshal(emptySnapshot{Entries: entries})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	if err := os.WriteFile(paths.StorePath, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func assertSecretResolves(t *testing.T, store *Store, ref secret.Reference, want string) {
	t.Helper()

	plaintext, err := store.Resolve(t.Context(), ref)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if string(plaintext) != want {
		t.Fatalf("Resolve() = %q, want %q", plaintext, want)
	}
}
