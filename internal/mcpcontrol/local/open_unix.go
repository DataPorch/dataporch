//go:build darwin || linux

package local

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func openCredential(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}

	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("opening local MCP credential: failed to create file")
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := validateCredentialInfo(info); err != nil {
		_ = file.Close()
		return nil, err
	}

	return file, nil
}

func validateParent(path string) error {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("parent is not a directory")
	}
	if info.Mode().Perm() != parentPermission {
		return fmt.Errorf("parent permissions are %o, want %o", info.Mode().Perm(), parentPermission)
	}
	if err := validateOwner(info); err != nil {
		return err
	}

	return nil
}

func validateCredentialInfo(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return errors.New("credential is not a regular file")
	}
	if info.Mode().Perm() != filePermission {
		return fmt.Errorf("credential permissions are %o, want %o", info.Mode().Perm(), filePermission)
	}
	if err := validateOwner(info); err != nil {
		return err
	}
	if info.Size() > maxCredentialSize {
		return errors.New("credential is too large")
	}

	return nil
}

func validateOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("credential owner metadata is unavailable")
	}
	if int(stat.Uid) != os.Geteuid() {
		return errors.New("credential owner is not the effective user")
	}

	return nil
}
