package localadmin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Server struct {
	path    string
	handler http.Handler
	server  *http.Server
}

func NewServer(path string, handler http.Handler, logger *slog.Logger) (*Server, error) {
	if path == "" {
		return nil, errors.New("local admin: socket path is required")
	}
	if handler == nil {
		return nil, errors.New("local admin: handler is required")
	}
	if logger == nil {
		return nil, errors.New("local admin: logger is required")
	}
	return &Server{path: path, handler: handler}, nil
}

func (s *Server) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("local admin: context is required")
	}
	if err := prepareSocket(s.path); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.path)
	if err != nil {
		return fmt.Errorf("listening on admin socket: %w", err)
	}
	if err := os.Chmod(s.path, 0o660); err != nil {
		listener.Close()
		os.Remove(s.path)
		return fmt.Errorf("setting admin socket permissions: %w", err)
	}
	defer os.Remove(s.path)
	s.server = &http.Server{Handler: s.handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = s.server.Shutdown(context.Background())
		case <-done:
		}
	}()
	err = s.server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func prepareSocket(path string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("creating admin socket directory: %w", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("local admin: socket directory is writable by group or world")
	}
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("local admin: existing socket path is not a socket")
	}
	connection, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err == nil {
		connection.Close()
		return errors.New("local admin: socket is already active")
	}
	var operation *net.OpError
	if !errors.As(err, &operation) || !errors.Is(operation.Err, syscall.ECONNREFUSED) {
		return fmt.Errorf("dialing existing admin socket: %w", err)
	}
	return os.Remove(path)
}
