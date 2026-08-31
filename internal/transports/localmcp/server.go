package localmcp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/DataPorch/dataporch/internal/mcpcontrol"
	"golang.org/x/sys/unix"
)

const (
	socketPermission       = 0o600
	parentPermission       = 0o700
	defaultShutdownTimeout = 5 * time.Second
)

var errRuntimeAlreadyActive = errors.New("local MCP: socket is already active")

type CredentialStore interface {
	Publish(string) error
	Delete() error
}

type Dependencies struct {
	SocketPath  string
	Credentials CredentialStore
	Random      io.Reader
	Handler     http.Handler
}

type Server struct {
	socketPath      string
	credentials     CredentialStore
	random          io.Reader
	handler         http.Handler
	shutdownTimeout time.Duration
}

func NewServer(dependencies Dependencies) (*Server, error) {
	if dependencies.SocketPath == "" {
		return nil, errors.New("local MCP: socket path is required")
	}
	if dependencies.Credentials == nil {
		return nil, errors.New("local MCP: credential store is required")
	}
	if dependencies.Random == nil {
		return nil, errors.New("local MCP: randomness source is required")
	}
	if dependencies.Handler == nil {
		return nil, errors.New("local MCP: handler is required")
	}

	return &Server{
		socketPath:      dependencies.SocketPath,
		credentials:     dependencies.Credentials,
		random:          dependencies.Random,
		handler:         dependencies.Handler,
		shutdownTimeout: defaultShutdownTimeout,
	}, nil
}

//nolint:gocyclo // Lifecycle ordering keeps credential and socket cleanup explicit.
func (s *Server) Run(ctx context.Context) (err error) {
	if ctx == nil {
		return errors.New("local MCP: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	runtimeLock, err := acquireRuntimeLock(s.socketPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := runtimeLock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("releasing local MCP runtime lock: %w", closeErr))
		}
	}()

	if err := prepareSocket(s.socketPath); err != nil {
		return err
	}

	listener, err := net.Listen("unix", s.socketPath) //nolint:noctx // Shutdown controls the listener lifetime.
	if err != nil {
		return fmt.Errorf("listening on local MCP socket: %w", err)
	}
	defer func() {
		if cleanupErr := cleanupSocket(listener, s.socketPath); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()

	if err := os.Chmod(s.socketPath, socketPermission); err != nil {
		return fmt.Errorf("setting local MCP socket permissions: %w", err)
	}

	credential, err := mcpcontrol.Generate(s.random)
	if err != nil {
		return err
	}
	if err := s.credentials.Publish(credential); err != nil {
		return fmt.Errorf("publishing local MCP credential: %w", err)
	}
	defer func() {
		if cleanupErr := s.credentials.Delete(); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("deleting local MCP credential: %w", cleanupErr))
		}
	}()

	authenticated := authenticatedHandler(credential, s.handler)
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		authenticated.ServeHTTP(w, r)
	})
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	done := make(chan struct{})
	shutdownErrors := make(chan error, 1)
	go func() {
		var shutdownErr error
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
			defer cancel()
			shutdownErr = server.Shutdown(shutdownContext)
			if shutdownErr != nil {
				shutdownErr = errors.Join(shutdownErr, server.Close())
			}
		case <-done:
		}
		shutdownErrors <- shutdownErr
	}()

	err = server.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if shutdownErr := <-shutdownErrors; shutdownErr != nil {
		err = errors.Join(err, fmt.Errorf("shutting down local MCP server: %w", shutdownErr))
	}

	return err
}

func prepareSocket(path string) error {
	if err := prepareParent(filepath.Dir(path)); err != nil {
		return fmt.Errorf("preparing local MCP socket directory: %w", err)
	}

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stating existing local MCP socket: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("local MCP: existing socket path is a symlink")
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("local MCP: existing socket path is not a socket")
	}
	if err := validateOwner(info); err != nil {
		return fmt.Errorf("validating existing local MCP socket: %w", err)
	}

	connection, err := net.DialTimeout("unix", path, 200*time.Millisecond) //nolint:noctx // The stale-socket probe is deliberately bounded.
	if err == nil {
		_ = connection.Close()
		return errRuntimeAlreadyActive
	}
	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("dialing existing local MCP socket: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing stale local MCP socket: %w", err)
	}

	return nil
}

func acquireRuntimeLock(socketPath string) (*os.File, error) {
	if err := prepareParent(filepath.Dir(socketPath)); err != nil {
		return nil, fmt.Errorf("preparing local MCP socket directory: %w", err)
	}

	lockPath := runtimeLockPath(socketPath)
	fd, err := unix.Open(
		lockPath,
		unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		uint32(socketPermission),
	)
	if err != nil {
		return nil, fmt.Errorf("opening local MCP runtime lock: %w", err)
	}

	file := os.NewFile(uintptr(fd), lockPath)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("opening local MCP runtime lock: failed to create file")
	}

	closeFile := func() {
		_ = file.Close()
	}
	info, err := file.Stat()
	if err != nil {
		closeFile()
		return nil, fmt.Errorf("stating local MCP runtime lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		closeFile()
		return nil, errors.New("local MCP runtime lock is not a regular file")
	}
	if info.Mode().Perm() != socketPermission {
		closeFile()
		return nil, fmt.Errorf("local MCP runtime lock permissions are %o, want %o", info.Mode().Perm(), socketPermission)
	}
	if err := validateOwner(info); err != nil {
		closeFile()
		return nil, fmt.Errorf("validating local MCP runtime lock: %w", err)
	}

	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeFile()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errRuntimeAlreadyActive
		}
		return nil, fmt.Errorf("locking local MCP runtime: %w", err)
	}

	return file, nil
}

func runtimeLockPath(socketPath string) string {
	// Keep the pathname stable; unlinking it during cleanup could bypass an active lock.
	digest := sha256.Sum256([]byte(socketPath))
	return filepath.Join(filepath.Dir(socketPath), fmt.Sprintf(".dataporch-mcp-%x.lock", digest))
}

func prepareParent(path string) error {
	if err := os.MkdirAll(path, parentPermission); err != nil {
		return err
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("local MCP socket parent is not a directory")
	}
	if info.Mode().Perm() != parentPermission {
		return fmt.Errorf("local MCP socket parent permissions are %o, want %o", info.Mode().Perm(), parentPermission)
	}
	if err := validateOwner(info); err != nil {
		return err
	}

	return nil
}

func cleanupSocket(listener net.Listener, path string) error {
	closeErr := listener.Close()
	if errors.Is(closeErr, net.ErrClosed) {
		closeErr = nil
	}
	removeErr := os.Remove(path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}

func validateOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("local MCP socket owner metadata is unavailable")
	}
	if int(stat.Uid) != os.Geteuid() {
		return errors.New("local MCP socket is not owned by the effective user")
	}

	return nil
}
