package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type temporaryFile interface {
	io.Writer
	Name() string
	Chmod(fs.FileMode) error
	Sync() error
	Close() error
}

type createTemporaryFile func(directory, pattern string) (temporaryFile, error)

func Create(path string, data []byte, permission fs.FileMode) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permission)
	if err != nil {
		return fmt.Errorf("creating file %q: %w", path, err)
	}

	var isClosed bool
	defer func() {
		if !isClosed {
			_ = file.Close()
		}
	}()

	if err := file.Chmod(permission); err != nil {
		return fmt.Errorf("setting permissions for %q: %w", path, err)
	}
	if err := writeAll(file, data); err != nil {
		return fmt.Errorf("writing file %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("syncing file %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing file %q: %w", path, err)
	}
	isClosed = true

	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("syncing directory for %q: %w", path, err)
	}

	return nil
}

func Replace(path string, data []byte, permission fs.FileMode) error {
	return replace(
		path,
		data,
		permission,
		func(directory, pattern string) (temporaryFile, error) {
			return os.CreateTemp(directory, pattern)
		},
	)
}

func replace(
	path string,
	data []byte,
	permission fs.FileMode,
	createTemporary createTemporaryFile,
) (err error) {
	if createTemporary == nil {
		return errors.New("atomicfile: temporary-file creator is required")
	}

	file, err := createTemporary(filepath.Dir(path), ".dataporch-*")
	if err != nil {
		return fmt.Errorf("creating temporary file for %q: %w", path, err)
	}
	if file == nil {
		return errors.New("atomicfile: temporary-file creator returned nil")
	}

	var isClosed bool
	var isPublished bool
	defer func() {
		if !isClosed {
			_ = file.Close()
		}
		if !isPublished {
			_ = os.Remove(file.Name())
		}
	}()

	if err := file.Chmod(permission); err != nil {
		return fmt.Errorf("setting temporary file permissions for %q: %w", path, err)
	}
	if err := writeAll(file, data); err != nil {
		return fmt.Errorf("writing temporary file for %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("syncing temporary file for %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing temporary file for %q: %w", path, err)
	}
	isClosed = true

	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("replacing file %q: %w", path, err)
	}
	isPublished = true

	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("syncing directory for %q: %w", path, err)
	}

	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}

	return nil
}

func syncDirectory(path string) (err error) {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}

	defer func() {
		closeErr := directory.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	return directory.Sync()
}
