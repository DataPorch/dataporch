package mcpstdio

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func newUnixTransport(socketPath string, credentials CredentialReader) *mcpsdk.StreamableClientTransport {
	return &mcpsdk.StreamableClientTransport{
		Endpoint:             "http://unix/mcp",
		HTTPClient:           unixHTTPClient(socketPath, credentials),
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
}

func unixHTTPClient(socketPath string, credentials CredentialReader) *http.Client {
	base := &http.Transport{
		DialContext: unixDialer(socketPath),
	}
	return &http.Client{Transport: newCredentialRoundTripper(base, credentials)}
}

func unixDialer(socketPath string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		if err := validateSocket(socketPath); err != nil {
			return nil, err
		}
		connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		if err != nil {
			return nil, runtimeUnavailable(err)
		}
		return connection, nil
	}
}

func validateSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return runtimeUnavailable(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("local MCP socket path is not an owner-only socket")
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("local MCP socket permissions are %o, want 600", info.Mode().Perm())
	}
	if err := validateSocketOwner(info); err != nil {
		return err
	}

	return nil
}

func validateSocketOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("local MCP socket owner metadata is unavailable")
	}
	if int(stat.Uid) != os.Geteuid() {
		return errors.New("local MCP socket is not owned by the effective user")
	}

	return nil
}

func runtimeUnavailable(err error) error {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
	}
	return err
}
