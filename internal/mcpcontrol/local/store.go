package local

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/DataPorch/dataporch/internal/atomicfile"
	"github.com/DataPorch/dataporch/internal/mcpcontrol"
)

const (
	filePermission        = 0o600
	parentPermission      = 0o700
	maxCredentialSize     = 1024
	maxCredentialReadSize = maxCredentialSize + 1
)

var ErrInvalidPath = errors.New("local MCP credential path is invalid")

type Store struct {
	path string
}

func New(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return nil, ErrInvalidPath
	}

	return &Store{path: path}, nil
}

func (s *Store) Publish(credential string) error {
	if s == nil || s.path == "" {
		return ErrInvalidPath
	}
	if err := mcpcontrol.Validate(credential); err != nil {
		return err
	}
	if err := validateParent(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("validating local MCP credential directory: %w", err)
	}
	if err := atomicfile.Replace(s.path, []byte(credential), filePermission); err != nil {
		return fmt.Errorf("publishing local MCP credential: %w", err)
	}

	return nil
}

func (s *Store) Read() (string, error) {
	if s == nil || s.path == "" {
		return "", ErrInvalidPath
	}
	if err := validateParent(filepath.Dir(s.path)); err != nil {
		return "", fmt.Errorf("validating local MCP credential directory: %w", err)
	}

	file, err := openCredential(s.path)
	if err != nil {
		return "", fmt.Errorf("opening local MCP credential: %w", err)
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxCredentialReadSize))
	if err != nil {
		return "", fmt.Errorf("reading local MCP credential: %w", err)
	}
	if len(data) > maxCredentialSize {
		return "", errors.New("local MCP credential is too large")
	}
	credential := string(data)
	if err := mcpcontrol.Validate(credential); err != nil {
		return "", err
	}

	return credential, nil
}

func (s *Store) Delete() error {
	if s == nil || s.path == "" {
		return ErrInvalidPath
	}
	if err := validateParent(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("validating local MCP credential directory: %w", err)
	}

	info, err := os.Lstat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stating local MCP credential for deletion: %w", err)
	}
	if err := validateCredentialInfo(info); err != nil {
		return fmt.Errorf("validating local MCP credential for deletion: %w", err)
	}
	if err := os.Remove(s.path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("deleting local MCP credential: %w", err)
	}

	return nil
}
