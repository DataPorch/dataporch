package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/adamraziv/dataporch/internal/atomicfile"
	"github.com/adamraziv/dataporch/internal/mcptoken"
)

const (
	fileVersion    = 1
	filePermission = 0o600
	maxStoreSize   = 16 << 10
)

var (
	ErrStoreCorrupt       = errors.New("mcp token store is corrupt")
	ErrInvalidPermissions = errors.New("mcp token store permissions are invalid")
	ErrInvalidPath        = errors.New("mcp token store path is invalid")
)

type Store struct {
	path string
}

type persistedFile struct {
	Version   int     `json:"version"`
	Verifier  string  `json:"verifier"`
	CreatedAt string  `json:"created_at"`
	RotatedAt *string `json:"rotated_at"`
}

var _ mcptoken.Store = (*Store)(nil)

func New(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidPath
	}

	return &Store{path: path}, nil
}

func (s *Store) Load(ctx context.Context) (mcptoken.PersistedState, bool, error) {
	if err := validContext(ctx); err != nil {
		return mcptoken.PersistedState{}, false, err
	}

	if s == nil || s.path == "" {
		return mcptoken.PersistedState{}, false, ErrInvalidPath
	}

	info, err := os.Lstat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return mcptoken.PersistedState{}, false, nil
	}

	if err != nil {
		return mcptoken.PersistedState{}, false, fmt.Errorf("stating mcp token store: %w", err)
	}

	if err := validateRegularFile(info); err != nil {
		return mcptoken.PersistedState{}, false, err
	}

	if info.Mode().Perm()&0o077 != 0 {
		return mcptoken.PersistedState{}, false, fmt.Errorf("%w: %s", ErrInvalidPermissions, s.path)
	}

	if info.Size() > maxStoreSize {
		return mcptoken.PersistedState{}, false, fmt.Errorf("%w: file is too large", ErrStoreCorrupt)
	}

	if err := validContext(ctx); err != nil {
		return mcptoken.PersistedState{}, false, err
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		return mcptoken.PersistedState{}, false, fmt.Errorf("reading mcp token store: %w", err)
	}

	if err := validContext(ctx); err != nil {
		return mcptoken.PersistedState{}, false, err
	}

	persisted, err := decode(data)
	if err != nil {
		return mcptoken.PersistedState{}, false, err
	}

	return persisted, true, nil
}

func (s *Store) Save(ctx context.Context, state mcptoken.PersistedState) error {
	if err := validContext(ctx); err != nil {
		return err
	}

	if s == nil || s.path == "" {
		return ErrInvalidPath
	}

	data, err := encode(state)
	if err != nil {
		return err
	}

	if err := validContext(ctx); err != nil {
		return err
	}

	if err := atomicfile.Replace(s.path, data, filePermission); err != nil {
		return fmt.Errorf("persisting mcp token store: %w", err)
	}

	return nil
}

func (s *Store) Delete(ctx context.Context) error {
	if err := validContext(ctx); err != nil {
		return err
	}

	if s == nil || s.path == "" {
		return ErrInvalidPath
	}

	info, err := os.Lstat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("stating mcp token store for deletion: %w", err)
	}

	if info.IsDir() {
		return fmt.Errorf("%w: refusing to delete directory", ErrStoreCorrupt)
	}

	if info.Mode()&os.ModeSymlink == 0 && !info.Mode().IsRegular() {
		return fmt.Errorf("%w: refusing to delete non-regular file", ErrStoreCorrupt)
	}

	if err := validContext(ctx); err != nil {
		return err
	}

	if err := os.Remove(s.path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("deleting mcp token store: %w", err)
	}

	return nil
}

func encode(state mcptoken.PersistedState) ([]byte, error) {
	var zeroVerifier [sha256.Size]byte
	if state.Verifier == zeroVerifier {
		return nil, fmt.Errorf("%w: verifier is empty", ErrStoreCorrupt)
	}

	createdAt, err := formatTimestamp(state.CreatedAt)
	if err != nil {
		return nil, err
	}

	rotatedAt, err := formatOptionalTimestamp(state.RotatedAt)
	if err != nil {
		return nil, err
	}

	if state.RotatedAt != nil && state.RotatedAt.Before(state.CreatedAt) {
		return nil, fmt.Errorf("%w: rotated_at precedes created_at", ErrStoreCorrupt)
	}

	file := persistedFile{
		Version:   fileVersion,
		Verifier:  base64.RawURLEncoding.EncodeToString(state.Verifier[:]),
		CreatedAt: createdAt,
		RotatedAt: rotatedAt,
	}

	data, err := json.Marshal(file)
	if err != nil {
		return nil, fmt.Errorf("encoding mcp token store: %w", err)
	}

	return data, nil
}

func decode(data []byte) (mcptoken.PersistedState, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var file persistedFile
	if err := decoder.Decode(&file); err != nil {
		return mcptoken.PersistedState{}, fmt.Errorf("%w: decoding json", ErrStoreCorrupt)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return mcptoken.PersistedState{}, fmt.Errorf("%w: trailing json", ErrStoreCorrupt)
	}

	if file.Version != fileVersion {
		return mcptoken.PersistedState{}, fmt.Errorf("%w: unsupported version", ErrStoreCorrupt)
	}

	verifierBytes, err := base64.RawURLEncoding.DecodeString(file.Verifier)
	if err != nil || len(verifierBytes) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(verifierBytes) != file.Verifier {
		return mcptoken.PersistedState{}, fmt.Errorf("%w: invalid verifier", ErrStoreCorrupt)
	}

	createdAt, err := parseTimestamp(file.CreatedAt)
	if err != nil {
		return mcptoken.PersistedState{}, fmt.Errorf("%w: invalid created_at", ErrStoreCorrupt)
	}

	rotatedAt, err := parseOptionalTimestamp(file.RotatedAt)
	if err != nil {
		return mcptoken.PersistedState{}, fmt.Errorf("%w: invalid rotated_at", ErrStoreCorrupt)
	}

	if rotatedAt != nil && rotatedAt.Before(createdAt) {
		return mcptoken.PersistedState{}, fmt.Errorf("%w: rotated_at precedes created_at", ErrStoreCorrupt)
	}

	var verifier [sha256.Size]byte
	copy(verifier[:], verifierBytes)

	return mcptoken.PersistedState{
		Verifier:  verifier,
		CreatedAt: createdAt,
		RotatedAt: rotatedAt,
	}, nil
}

func formatTimestamp(value time.Time) (string, error) {
	if value.IsZero() {
		return "", fmt.Errorf("%w: created_at is zero", ErrStoreCorrupt)
	}

	return value.UTC().Format(time.RFC3339Nano), nil
}

func formatOptionalTimestamp(value *time.Time) (*string, error) {
	if value == nil {
		return nil, nil
	}

	formatted, err := formatTimestamp(*value)
	if err != nil {
		return nil, fmt.Errorf("%w: rotated_at is zero", ErrStoreCorrupt)
	}

	return &formatted, nil
}

func parseTimestamp(value string) (time.Time, error) {
	if value == "" || !strings.HasSuffix(value, "Z") {
		return time.Time{}, errors.New("timestamp must be UTC RFC3339")
	}

	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() {
		return time.Time{}, errors.New("timestamp is invalid")
	}

	return parsed, nil
}

func parseOptionalTimestamp(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}

	parsed, err := parseTimestamp(*value)
	if err != nil {
		return nil, err
	}

	return &parsed, nil
}

func validateRegularFile(info fs.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: not a regular file", ErrStoreCorrupt)
	}

	return nil
}

func validContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("mcp token store context is nil")
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mcp token store context: %w", err)
	}

	return nil
}
