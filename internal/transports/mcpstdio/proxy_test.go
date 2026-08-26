package mcpstdio

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProxyPreservesRawMessages(t *testing.T) {
	t.Parallel()

	downstream, downstreamPeer := mcpsdk.NewInMemoryTransports()
	upstream, upstreamPeer := mcpsdk.NewInMemoryTransports()
	proxy := newProxy(downstream, upstream)
	ctx, cancel := context.WithCancel(t.Context())
	runErr := make(chan error, 1)
	go func() { runErr <- proxy.Run(ctx) }()

	client, err := downstreamPeer.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect(client) error = %v", err)
	}
	server, err := upstreamPeer.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect(server) error = %v", err)
	}

	request, err := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("DecodeMessage(request) error = %v", err)
	}
	if err := client.Write(ctx, request); err != nil {
		t.Fatalf("Write(request) error = %v", err)
	}
	forwarded, err := server.Read(ctx)
	if err != nil {
		t.Fatalf("Read(request) error = %v", err)
	}
	assertEncodedMessage(t, forwarded, request)

	response, err := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`))
	if err != nil {
		t.Fatalf("DecodeMessage(response) error = %v", err)
	}
	if err := server.Write(ctx, response); err != nil {
		t.Fatalf("Write(response) error = %v", err)
	}
	forwarded, err = client.Read(ctx)
	if err != nil {
		t.Fatalf("Read(response) error = %v", err)
	}
	assertEncodedMessage(t, forwarded, response)

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}

func TestProxyStopsWhenDownstreamCloses(t *testing.T) {
	t.Parallel()

	downstream, downstreamPeer := mcpsdk.NewInMemoryTransports()
	upstream, _ := mcpsdk.NewInMemoryTransports()
	proxy := newProxy(downstream, upstream)
	runErr := make(chan error, 1)
	go func() { runErr <- proxy.Run(t.Context()) }()
	client, err := downstreamPeer.Connect(t.Context())
	if err != nil {
		t.Fatalf("Connect(client) error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close(client) error = %v", err)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after downstream close")
	}
}

func TestProxyCancellationUnblocksBothReads(t *testing.T) {
	t.Parallel()

	downstream, _ := mcpsdk.NewInMemoryTransports()
	upstream, _ := mcpsdk.NewInMemoryTransports()
	proxy := newProxy(downstream, upstream)
	ctx, cancel := context.WithCancel(t.Context())
	runErr := make(chan error, 1)
	go func() { runErr <- proxy.Run(ctx) }()
	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}

func assertEncodedMessage(t *testing.T, got, want jsonrpc.Message) {
	t.Helper()
	gotData, err := jsonrpc.EncodeMessage(got)
	if err != nil {
		t.Fatalf("EncodeMessage(got) error = %v", err)
	}
	wantData, err := jsonrpc.EncodeMessage(want)
	if err != nil {
		t.Fatalf("EncodeMessage(want) error = %v", err)
	}
	if string(gotData) != string(wantData) {
		t.Fatalf("encoded message = %s, want %s", gotData, wantData)
	}
}

type credentialSequence struct {
	values []string
	reads  int
	err    error
}

func (r *credentialSequence) Read() (string, error) {
	r.reads++
	if r.err != nil {
		return "", r.err
	}
	if len(r.values) == 0 {
		return "", errors.New("credential sequence exhausted")
	}
	value := r.values[0]
	r.values = r.values[1:]
	return value, nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newRequest(t *testing.T, withGetBody bool) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://unix/mcp", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if !withGetBody {
		request.GetBody = nil
	}
	return request
}
