package mcpstdio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var ErrRuntimeUnavailable = errors.New("local MCP runtime unavailable")

type CredentialReader interface {
	Read() (string, error)
}

type Dependencies struct {
	Input       io.Reader
	Output      io.Writer
	SocketPath  string
	Credentials CredentialReader
}

type Proxy struct {
	downstream mcpsdk.Transport
	upstream   mcpsdk.Transport
}

func New(dependencies Dependencies) (*Proxy, error) {
	if dependencies.Input == nil {
		return nil, errors.New("MCP stdio input is required")
	}
	if dependencies.Output == nil {
		return nil, errors.New("MCP stdio output is required")
	}
	if dependencies.SocketPath == "" {
		return nil, errors.New("MCP stdio socket path is required")
	}
	if dependencies.Credentials == nil {
		return nil, errors.New("MCP stdio credential reader is required")
	}

	return newProxy(
		&mcpsdk.IOTransport{
			Reader: readCloser{Reader: dependencies.Input},
			Writer: writeCloser{Writer: dependencies.Output},
		},
		newUnixTransport(dependencies.SocketPath, dependencies.Credentials),
	), nil
}

func newProxy(downstream, upstream mcpsdk.Transport) *Proxy {
	return &Proxy{downstream: downstream, upstream: upstream}
}

func (p *Proxy) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("MCP stdio context is required")
	}

	downstream, err := p.downstream.Connect(ctx)
	if err != nil {
		return fmt.Errorf("connecting MCP stdio: %w", err)
	}
	upstream, err := p.upstream.Connect(ctx)
	if err != nil {
		_ = downstream.Close()
		return fmt.Errorf("connecting local MCP runtime: %w", err)
	}

	return runProxy(ctx, downstream, upstream)
}

func runProxy(ctx context.Context, downstream, upstream mcpsdk.Connection) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan error, 2)
	go forward(runCtx, downstream, upstream, results)
	go forward(runCtx, upstream, downstream, results)

	first := <-results
	cancel()
	closeErr := errors.Join(downstream.Close(), upstream.Close())
	second := <-results
	if isNormalProxyExit(first) && isNormalProxyExit(second) {
		return closeErr
	}

	return errors.Join(proxyError(first), proxyError(second), closeErr)
}

func forward(ctx context.Context, source, destination mcpsdk.Connection, results chan<- error) {
	for {
		message, err := source.Read(ctx)
		if err != nil {
			results <- err
			return
		}
		if err := destination.Write(ctx, message); err != nil {
			results <- err
			return
		}
	}
}

func isNormalProxyExit(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, mcpsdk.ErrConnectionClosed)
}

func proxyError(err error) error {
	if isNormalProxyExit(err) {
		return nil
	}
	return err
}

type readCloser struct{ io.Reader }

func (readCloser) Close() error { return nil }

type writeCloser struct{ io.Writer }

func (writeCloser) Close() error { return nil }
