package local

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/adamraziv/dataporch/internal/atomicfile"
	"github.com/adamraziv/dataporch/internal/secret"
)

const secretIDSize = 18

var (
	ErrSecretNotFound     = errors.New("secret local: secret not found")
	ErrWrongProvider      = errors.New("secret local: wrong secret provider")
	ErrStoreCorrupt       = errors.New("secret local: secret store is corrupt")
	ErrInvalidPermissions = errors.New("secret local: invalid file permissions")
	errContextRequired    = errors.New("secret local: context is required")
)

type Store struct {
	mu            sync.RWMutex
	path          string
	aead          cipher.AEAD
	entries       map[string][]byte
	writeSnapshot func(string, map[string][]byte) error
}

func Open(paths Paths) (*Store, error) {
	if err := validatePaths(paths); err != nil {
		return nil, err
	}

	key, err := readProtectedFile(paths.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("reading master key: %w", err)
	}
	defer clear(key)
	if len(key) != masterKeySize {
		return nil, fmt.Errorf("%w: invalid master key length", ErrStoreCorrupt)
	}

	storeData, err := readProtectedFile(paths.StorePath)
	if err != nil {
		return nil, fmt.Errorf("reading secret store: %w", err)
	}

	var snapshot emptySnapshot
	if err := json.Unmarshal(storeData, &snapshot); err != nil || snapshot.Entries == nil {
		return nil, fmt.Errorf("%w: invalid encrypted entries", ErrStoreCorrupt)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("creating authenticated cipher: %w", err)
	}

	return &Store{
		path:          paths.StorePath,
		aead:          aead,
		entries:       cloneEntries(snapshot.Entries),
		writeSnapshot: writeSnapshot,
	}, nil
}

func (s *Store) Store(ctx context.Context, plaintext []byte) (secret.Reference, error) {
	if err := validContext(ctx); err != nil {
		return "", err
	}

	idBytes := make([]byte, secretIDSize)
	defer clear(idBytes)
	if _, err := rand.Read(idBytes); err != nil {
		return "", fmt.Errorf("generating secret id: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	ref, err := secret.NewLocal(id)
	if err != nil {
		return "", fmt.Errorf("creating secret reference: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ciphertext := s.aead.Seal(
		nil,
		nil,
		plaintext,
		idBytes,
	)
	entries := cloneEntries(s.entries)
	entries[id] = ciphertext
	if err := s.writeSnapshot(s.path, entries); err != nil {
		return "", fmt.Errorf("persisting encrypted secret: %w", err)
	}

	s.entries = entries
	return ref, nil
}

func (s *Store) Resolve(ctx context.Context, ref secret.Reference) ([]byte, error) {
	if err := validContext(ctx); err != nil {
		return nil, err
	}

	id, err := localID(ref)
	if err != nil {
		return nil, err
	}
	idBytes, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid secret id", ErrStoreCorrupt)
	}
	defer clear(idBytes)

	s.mu.RLock()
	ciphertext, exists := s.entries[id]
	if exists {
		ciphertext = append([]byte(nil), ciphertext...)
	}
	aead := s.aead
	s.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrSecretNotFound, id)
	}

	plaintext, err := aead.Open(
		nil,
		nil,
		ciphertext,
		idBytes,
	)
	clear(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("%w: decrypting secret %s", ErrStoreCorrupt, id)
	}

	return append([]byte(nil), plaintext...), nil
}

func (s *Store) Delete(ctx context.Context, ref secret.Reference) error {
	if err := validContext(ctx); err != nil {
		return err
	}

	id, err := localID(ref)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.entries[id]; !exists {
		return fmt.Errorf("%w: %s", ErrSecretNotFound, id)
	}

	entries := cloneEntries(s.entries)
	delete(entries, id)
	if err := s.writeSnapshot(s.path, entries); err != nil {
		return fmt.Errorf("persisting encrypted secret deletion: %w", err)
	}

	s.entries = entries
	return nil
}

func localID(ref secret.Reference) (string, error) {
	scheme, id, err := ref.Parts()
	if err != nil {
		return "", fmt.Errorf("parsing secret reference: %w", err)
	}
	if scheme != "local" {
		return "", fmt.Errorf("%w: %s", ErrWrongProvider, scheme)
	}

	return id, nil
}

func readProtectedFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: not a regular file", ErrStoreCorrupt)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPermissions, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func writeSnapshot(path string, entries map[string][]byte) error {
	data, err := json.Marshal(emptySnapshot{Entries: entries})
	if err != nil {
		return fmt.Errorf("encoding encrypted secret store: %w", err)
	}

	return atomicfile.Replace(path, data, 0o600)
}

func cloneEntries(entries map[string][]byte) map[string][]byte {
	cloned := make(map[string][]byte, len(entries))
	for id, ciphertext := range entries {
		cloned[id] = append([]byte(nil), ciphertext...)
	}

	return cloned
}

func validContext(ctx context.Context) error {
	if ctx == nil {
		return errContextRequired
	}

	return ctx.Err()
}
