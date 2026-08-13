package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	protocolRevision20260728 = "2026-07-28"
	headerMismatchCode       = -32020
)

type modernRequest struct {
	method      string
	name        string
	clientName  string
	metaVersion string
}

type protocolResult struct {
	status int
	header http.Header
	body   []byte
}

type jsonRPCErrorEnvelope struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newProtocolTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	handler, err := New(newMCPTestDependencies(logger))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return server
}

func newModernRequest(
	t *testing.T,
	endpoint string,
	options modernRequest,
) *http.Request {
	t.Helper()

	params := map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion": options.metaVersion,
			"io.modelcontextprotocol/clientInfo": map[string]any{
				"name":    options.clientName,
				"version": "dev",
			},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	}
	if options.name != "" {
		params["name"] = options.name
		params["arguments"] = map[string]any{}
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  options.method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}

	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("MCP-Protocol-Version", options.metaVersion)
	request.Header.Set("Mcp-Method", options.method)

	if options.name != "" {
		request.Header.Set("Mcp-Name", options.name)
	}

	return request
}

func doProtocolRequest(
	t *testing.T,
	client *http.Client,
	request *http.Request,
) protocolResult {
	t.Helper()

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()

	if readErr != nil {
		t.Fatalf("ReadAll() error = %v", readErr)
	}

	if closeErr != nil {
		t.Fatalf("Body.Close() error = %v", closeErr)
	}

	return protocolResult{
		status: response.StatusCode,
		header: response.Header.Clone(),
		body:   body,
	}
}

func assertHeaderMismatch(t *testing.T, result protocolResult) {
	t.Helper()

	if result.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", result.status, http.StatusBadRequest)
	}

	if contentType := result.header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}

	var envelope jsonRPCErrorEnvelope

	if err := json.Unmarshal(result.body, &envelope); err != nil {
		t.Fatalf("Unmarshal() error = %v; body = %s", err, result.body)
	}

	if envelope.Error == nil || envelope.Error.Code != headerMismatchCode {
		t.Fatalf("error = %#v, want code %d", envelope.Error, headerMismatchCode)
	}
}

func TestHandlerValidatesProtocolVersionMetadata(t *testing.T) {
	t.Parallel()

	server := newProtocolTestServer(t)
	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{
			name: "missing protocol header",
			mutate: func(request *http.Request) {
				request.Header.Del("MCP-Protocol-Version")
			},
		},
		{
			name: "mismatched protocol header",
			mutate: func(request *http.Request) {
				request.Header.Set("MCP-Protocol-Version", "2026-07-29")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := newModernRequest(t, server.URL, modernRequest{
				method:      "tools/list",
				clientName:  "dataporch-conformance-test",
				metaVersion: protocolRevision20260728,
			})
			tt.mutate(request)

			result := doProtocolRequest(t, server.Client(), request)
			assertHeaderMismatch(t, result)
		})
	}
}

func TestHandlerValidatesRoutingMetadata(t *testing.T) {
	t.Parallel()

	server := newProtocolTestServer(t)
	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{
			name: "missing method",
			mutate: func(request *http.Request) {
				request.Header.Del("Mcp-Method")
			},
		},
		{
			name: "mismatched method",
			mutate: func(request *http.Request) {
				request.Header.Set("Mcp-Method", "prompts/get")
			},
		},
		{
			name: "missing required name",
			mutate: func(request *http.Request) {
				request.Header.Del("Mcp-Name")
			},
		},
		{
			name: "mismatched required name",
			mutate: func(request *http.Request) {
				request.Header.Set("Mcp-Name", "unknown.tool")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := newModernRequest(t, server.URL, modernRequest{
				method:      "tools/call",
				name:        "data_source.list",
				clientName:  "dataporch-conformance-test",
				metaVersion: protocolRevision20260728,
			})
			tt.mutate(request)

			result := doProtocolRequest(t, server.Client(), request)
			assertHeaderMismatch(t, result)
		})
	}
}

func TestHandlerAllowsNameOnlyWhenRequired(t *testing.T) {
	t.Parallel()

	server := newProtocolTestServer(t)
	tests := []modernRequest{
		{
			method:      "tools/list",
			clientName:  "dataporch-conformance-test",
			metaVersion: protocolRevision20260728,
		},
		{
			method:      "tools/call",
			name:        "data_source.list",
			clientName:  "dataporch-conformance-test",
			metaVersion: protocolRevision20260728,
		},
	}

	for _, options := range tests {
		t.Run(options.method, func(t *testing.T) {
			t.Parallel()

			request := newModernRequest(t, server.URL, options)

			result := doProtocolRequest(t, server.Client(), request)
			if result.status != http.StatusOK {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					result.status,
					http.StatusOK,
					result.body,
				)
			}
		})
	}
}

func TestHandlerKeepsModernRequestsIndependent(t *testing.T) {
	t.Parallel()

	server := newProtocolTestServer(t)

	clientNames := []string{"client-one", "client-two"}
	for _, clientName := range clientNames {
		request := newModernRequest(t, server.URL, modernRequest{
			method:      "tools/list",
			clientName:  clientName,
			metaVersion: protocolRevision20260728,
		})
		request.Header.Set("Mcp-Session-Id", "ignored-session")

		result := doProtocolRequest(t, server.Client(), request)
		if result.status != http.StatusOK {
			t.Fatalf(
				"client %q status = %d, want %d; body = %s",
				clientName,
				result.status,
				http.StatusOK,
				result.body,
			)
		}

		if sessionID := result.header.Get("Mcp-Session-Id"); sessionID != "" {
			t.Fatalf("client %q response session id = %q, want empty", clientName, sessionID)
		}
	}
}

func TestHandlerRejectsUnsupportedHTTPShapes(t *testing.T) {
	t.Parallel()

	server := newProtocolTestServer(t)
	tests := []struct {
		name       string
		request    func(*testing.T) *http.Request
		wantStatus int
		wantAllow  string
	}{
		{
			name: "get",
			request: func(t *testing.T) *http.Request {
				t.Helper()

				return newRequest(t, http.MethodGet, server.URL, nil)
			},
			wantStatus: http.StatusMethodNotAllowed,
			wantAllow:  http.MethodPost,
		},
		{
			name: "delete",
			request: func(t *testing.T) *http.Request {
				t.Helper()

				return newRequest(t, http.MethodDelete, server.URL, nil)
			},
			wantStatus: http.StatusMethodNotAllowed,
			wantAllow:  http.MethodPost,
		},
		{
			name: "invalid content type",
			request: func(t *testing.T) *http.Request {
				t.Helper()

				request := newRequest(t, http.MethodPost, server.URL, strings.NewReader("{}"))
				request.Header.Set("Accept", "application/json, text/event-stream")
				request.Header.Set("Content-Type", "text/plain")

				return request
			},
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "incomplete accept header",
			request: func(t *testing.T) *http.Request {
				t.Helper()

				request := newRequest(t, http.MethodPost, server.URL, strings.NewReader("{}"))
				request.Header.Set("Accept", "application/json")
				request.Header.Set("Content-Type", "application/json")

				return request
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "oversized body",
			request: func(t *testing.T) *http.Request {
				t.Helper()

				body := bytes.NewReader(bytes.Repeat([]byte{'x'}, maxRequestBodyBytes+1))
				request := newRequest(t, http.MethodPost, server.URL, body)
				request.Header.Set("Accept", "application/json, text/event-stream")
				request.Header.Set("Content-Type", "application/json")

				return request
			},
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := doProtocolRequest(t, server.Client(), tt.request(t))
			if result.status != tt.wantStatus {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					result.status,
					tt.wantStatus,
					result.body,
				)
			}

			if allow := result.header.Get("Allow"); allow != tt.wantAllow {
				t.Errorf("Allow = %q, want %q", allow, tt.wantAllow)
			}
		})
	}
}

func newRequest(
	t *testing.T,
	method string,
	endpoint string,
	body io.Reader,
) *http.Request {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), method, endpoint, body)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}

	return request
}

func TestHandlerReportsUnsupportedProtocolVersion(t *testing.T) {
	t.Parallel()

	server := newProtocolTestServer(t)
	request := newModernRequest(t, server.URL, modernRequest{
		method:      "tools/list",
		clientName:  "dataporch-conformance-test",
		metaVersion: "2025-01-01",
	})
	result := doProtocolRequest(t, server.Client(), request)

	if result.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", result.status, http.StatusBadRequest)
	}

	if !bytes.Contains(result.body, []byte("supported versions")) {
		t.Fatalf("body = %s, want supported versions", result.body)
	}
}

func TestHandlerReturnsNotFoundForUnknownModernMethod(t *testing.T) {
	t.Parallel()

	server := newProtocolTestServer(t)
	request := newModernRequest(t, server.URL, modernRequest{
		method:      "unknown/method",
		clientName:  "dataporch-conformance-test",
		metaVersion: protocolRevision20260728,
	})
	result := doProtocolRequest(t, server.Client(), request)

	if result.status != http.StatusNotFound {
		t.Fatalf(
			"status = %d, want %d; body = %s",
			result.status,
			http.StatusNotFound,
			result.body,
		)
	}
}

func TestHandlerRetainsLocalhostHostProtection(t *testing.T) {
	t.Parallel()

	server := newProtocolTestServer(t)
	request := newModernRequest(t, server.URL, modernRequest{
		method:      "tools/list",
		clientName:  "dataporch-conformance-test",
		metaVersion: protocolRevision20260728,
	})
	request.Host = "attacker.example"
	result := doProtocolRequest(t, server.Client(), request)

	if result.status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", result.status, http.StatusForbidden)
	}
}

type recordingRoundTripper struct {
	base http.RoundTripper

	mutex                sync.Mutex
	methods              []string
	hasResponseSessionID bool
}

func (r *recordingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}

	if err := request.Body.Close(); err != nil {
		return nil, fmt.Errorf("closing request body: %w", err)
	}

	request.Body = io.NopCloser(bytes.NewReader(body))
	request.Header.Set("Mcp-Session-Id", "ignored-session")

	var envelope struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decoding request method: %w", err)
	}

	response, err := r.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}

	r.mutex.Lock()
	r.methods = append(r.methods, envelope.Method)

	if response.Header.Get("Mcp-Session-Id") != "" {
		r.hasResponseSessionID = true
	}
	r.mutex.Unlock()

	return response, nil
}

func (r *recordingRoundTripper) snapshot() ([]string, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return append([]string{}, r.methods...), r.hasResponseSessionID
}

func TestHandlerSupportsModernClientWithoutProtocolSessions(t *testing.T) {
	t.Parallel()

	server := newProtocolTestServer(t)
	recorder := &recordingRoundTripper{base: server.Client().Transport}
	httpClient := &http.Client{Transport: recorder}
	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{
			Name:    "dataporch-modern-client-test",
			Version: "dev",
		},
		nil,
	)

	session, err := client.Connect(
		t.Context(),
		&mcpsdk.StreamableClientTransport{
			Endpoint:             server.URL,
			HTTPClient:           httpClient,
			DisableStandaloneSSE: true,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	isClosed := false

	t.Cleanup(func() {
		if isClosed {
			return
		}

		if err := session.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	if _, err := session.ListTools(t.Context(), nil); err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	isClosed = true

	methods, hasResponseSessionID := recorder.snapshot()

	if want := []string{"server/discover", "tools/list"}; !reflect.DeepEqual(methods, want) {
		t.Fatalf("methods = %v, want %v", methods, want)
	}

	if hasResponseSessionID {
		t.Fatal("server emitted Mcp-Session-Id in stateless mode")
	}
}
