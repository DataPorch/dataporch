package local

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/DataPorch/dataporch/internal/atomicfile"
)

const masterKeySize = 32

var (
	ErrAlreadyInitialized = errors.New("secret local: already initialized")
	errPathsRequired      = errors.New("secret local: key and store paths are required")
)

type Paths struct {
	KeyPath   string
	StorePath string
}

type createFileFunc func(path string, data []byte, permission fs.FileMode) error

type emptySnapshot struct {
	Entries map[string][]byte `json:"entries"`
}

func Init(paths Paths) error {
	return initStore(paths, rand.Reader, atomicfile.Create)
}

func initStore(paths Paths, random io.Reader, create createFileFunc) error {
	if err := validatePaths(paths); err != nil {
		return err
	}

	if random == nil {
		return errors.New("secret local: random reader is required")
	}

	if create == nil {
		return errors.New("secret local: file creator is required")
	}

	if err := ensurePrivateDirectory(filepath.Dir(paths.KeyPath)); err != nil {
		return fmt.Errorf("creating master key directory: %w", err)
	}

	if err := ensurePrivateDirectory(filepath.Dir(paths.StorePath)); err != nil {
		return fmt.Errorf("creating secret store directory: %w", err)
	}

	key := make([]byte, masterKeySize)
	defer zeroBytes(key)

	if _, err := io.ReadFull(random, key); err != nil {
		return fmt.Errorf("generating master key: %w", err)
	}

	snapshot, err := json.Marshal(emptySnapshot{Entries: map[string][]byte{}})
	if err != nil {
		return fmt.Errorf("encoding empty secret store: %w", err)
	}

	if err := create(paths.KeyPath, key, 0o600); err != nil {
		return initializedError(err)
	}

	if err := create(paths.StorePath, snapshot, 0o600); err != nil {
		if removeErr := os.Remove(paths.KeyPath); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			return errors.Join(initializedError(err), fmt.Errorf("removing new master key: %w", removeErr))
		}

		return initializedError(err)
	}

	return nil
}

func validatePaths(paths Paths) error {
	if paths.KeyPath == "" || paths.StorePath == "" {
		return errPathsRequired
	}

	if paths.KeyPath == paths.StorePath {
		return errors.New("secret local: key and store paths must differ")
	}

	return nil
}

func initializedError(err error) error {
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: local key or store exists", ErrAlreadyInitialized)
	}

	return err
}

func ensurePrivateDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("secret local: private directory must be absolute")
	}
	clean := filepath.Clean(path)
	root := string(filepath.Separator)
	current := root
	for component := range strings.SplitSeq(strings.TrimPrefix(clean, root), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return fmt.Errorf("creating private directory %q: %w", current, err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("inspecting private directory %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("secret local: unsafe directory component %q", current)
		}
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return fmt.Errorf("inspecting private directory %q: %w", clean, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("secret local: private directory %q must be owner-only", clean)
	}
	return nil
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
