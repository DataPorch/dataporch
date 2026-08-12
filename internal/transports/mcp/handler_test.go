package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/execution"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNewValidatesDependencies(t *testing.T) {
	t.Parallel()

	var typedNil *recordingDiscoverer

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	if _, err := New(nil, logger); !errors.Is(err, errDiscovererRequired) {
		t.Fatalf("New(nil) error = %v, want discoverer validation", err)
	}

	if _, err := New(typedNil, logger); !errors.Is(err, errDiscovererRequired) {
		t.Fatalf("New(typed nil) error = %v, want discoverer validation", err)
	}

	if _, err := New(&recordingDiscoverer{}, nil); !errors.Is(err, errLoggerRequired) {
		t.Fatalf("New(nil logger) error = %v, want logger validation", err)
	}
}

//nolint:gocyclo // This protocol test covers registration, negotiation, schemas, and annotations together.
func TestHandlerListsFourDiscoveryToolsAndNegotiatesLatestProtocol(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	handler, err := New(&recordingDiscoverer{}, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	session := connectTestSession(t, server, "dev")

	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("protocol version = %q, want 2026-07-28", got)
	}

	result, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}

	wantNames := []string{
		"data_source.list",
		"relational_database.list_columns",
		"relational_database.list_schemas",
		"relational_database.list_tables",
	}

	gotNames := make([]string, len(result.Tools))
	for index, tool := range result.Tools {
		gotNames[index] = tool.Name
	}

	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("tool names = %v, want %v", gotNames, wantNames)
	}

	for _, tool := range result.Tools {
		if tool.Name == "list_resources" {
			t.Fatal("old list_resources tool is still registered")
		}

		annotationsAreSafe := tool.Annotations != nil &&
			tool.Annotations.ReadOnlyHint &&
			tool.Annotations.IdempotentHint &&
			tool.Annotations.DestructiveHint != nil &&
			!*tool.Annotations.DestructiveHint &&
			tool.Annotations.OpenWorldHint != nil &&
			!*tool.Annotations.OpenWorldHint
		if !annotationsAreSafe {
			t.Fatalf("annotations for %q = %#v", tool.Name, tool.Annotations)
		}

		if tool.OutputSchema == nil {
			t.Fatalf("output schema for %q is nil", tool.Name)
		}
	}

	schemas := make(map[string]map[string]any)

	for _, tool := range result.Tools {
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s schema: %v", tool.Name, err)
		}

		var schema map[string]any
		if err := json.Unmarshal(encoded, &schema); err != nil {
			t.Fatalf("unmarshal %s schema: %v", tool.Name, err)
		}

		schemas[tool.Name] = schema
	}

	schemasAreComplete := hasRequired(
		schemas["relational_database.list_schemas"],
		"source_id",
	) && hasRequired(
		schemas["relational_database.list_tables"],
		"source_id",
		"schema",
	) && hasRequired(
		schemas["relational_database.list_columns"],
		"source_id",
		"schema",
		"table",
	)
	if !schemasAreComplete {
		t.Fatalf("required fields missing from inferred schemas: %#v", schemas)
	}
}

func TestHandlerReturnsStructuredAndTextSuccess(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	service := &recordingDiscoverer{
		sourcesResult: execution.ListDataSourcesResult{
			Sources: []execution.DataSource{
				{
					ID:           "analytics",
					Kind:         "postgres",
					Capabilities: []execution.Capability{execution.CapabilityRelationalDatabase},
				},
			},
		},
	}

	handler, err := New(service, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	session := connectTestSession(t, server, "dev")

	result, err := session.CallTool(
		t.Context(),
		&mcpsdk.CallToolParams{
			Name:      "data_source.list",
			Arguments: map[string]any{},
		},
	)
	if err != nil || result.IsError {
		t.Fatalf("CallTool() result/error = %#v/%v", result, err)
	}

	if len(result.Content) != 1 {
		t.Fatalf("content count = %d, want one JSON text block", len(result.Content))
	}

	textContent, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want TextContent", result.Content[0])
	}

	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}

	if string(structured) != textContent.Text {
		t.Fatalf("structured/text mismatch: %s != %s", structured, textContent.Text)
	}
}

//nolint:gocyclo // This protocol test covers schema, tool, and malformed-request errors together.
func TestHandlerSeparatesProtocolAndToolErrors(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	handler, err := New(&recordingDiscoverer{}, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	session := connectTestSession(t, server, serverVersion)

	validationResult, err := session.CallTool(
		t.Context(),
		&mcpsdk.CallToolParams{
			Name:      "relational_database.list_schemas",
			Arguments: map[string]any{},
		},
	)
	if err != nil {
		t.Fatalf("schema validation error = %v, want tool error", err)
	}

	validationIsSafe := validationResult != nil &&
		validationResult.IsError &&
		validationResult.StructuredContent == nil &&
		len(validationResult.Content) == 1
	if !validationIsSafe {
		t.Fatalf("schema validation result = %#v, want safe tool error", validationResult)
	}

	unknownResult, err := session.CallTool(
		t.Context(),
		&mcpsdk.CallToolParams{
			Name:      "unknown.tool",
			Arguments: map[string]any{},
		},
	)
	if err == nil || unknownResult != nil {
		t.Fatalf("unknown tool result/error = %#v/%v, want protocol error", unknownResult, err)
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, strings.NewReader("{"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("malformed request error = %v", err)
	}

	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("malformed response Body.Close() error = %v", err)
		}
	})

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}

	responseIsSafe := response.StatusCode == http.StatusBadRequest &&
		bytes.Contains(body, []byte("malformed payload")) &&
		!bytes.Contains(body, []byte(`"result"`))
	if !responseIsSafe {
		t.Fatalf("malformed response = %d/%s, want JSON-RPC protocol error", response.StatusCode, body)
	}
}

func TestHandlerReturnsSafeToolErrors(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	service := &recordingDiscoverer{sourcesErr: errors.New("host=private password=secret")}

	handler, err := New(service, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	session := connectTestSession(t, server, "dev")

	result, err := session.CallTool(
		t.Context(),
		&mcpsdk.CallToolParams{
			Name:      "data_source.list",
			Arguments: map[string]any{},
		},
	)
	if err != nil || !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("error result/error = %#v/%v", result, err)
	}

	textContent, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want TextContent", result.Content[0])
	}

	textIsUnsafe := textContent.Text == "" ||
		bytes.Contains([]byte(textContent.Text), []byte("private")) ||
		bytes.Contains([]byte(textContent.Text), []byte("secret"))
	if textIsUnsafe {
		t.Fatalf("unsafe error text = %q", textContent.Text)
	}
}

func TestHandlerPropagatesCancellation(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	cancelService := &recordingDiscoverer{
		waitForCancellation: true,
		started:             make(chan struct{}),
		cancelled:           make(chan struct{}),
	}

	cancelHandler, err := New(cancelService, logger)
	if err != nil {
		t.Fatalf("New(cancel) error = %v", err)
	}

	cancelServer := httptest.NewServer(cancelHandler)
	defer cancelServer.Close()

	cancelSession := connectTestSession(t, cancelServer, "dev")

	ctx, cancel := context.WithCancel(t.Context())
	callDone := make(chan struct{})

	go func() {
		_, _ = cancelSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "data_source.list", Arguments: map[string]any{}})

		close(callDone)
	}()

	<-cancelService.started
	cancel()

	cancellationTimeout := time.NewTimer(time.Second)
	defer cancellationTimeout.Stop()

	select {
	case <-cancelService.cancelled:
	case <-cancellationTimeout.C:
		t.Fatal("execution did not observe cancellation")
	}

	select {
	case <-callDone:
	case <-time.After(time.Second):
		t.Fatal("call did not return after execution observed cancellation")
	}
}

func connectTestSession(
	t *testing.T,
	server *httptest.Server,
	version string,
) *mcpsdk.ClientSession {
	t.Helper()

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{
			Name:    "dataporch-test",
			Version: version,
		},
		nil,
	)

	session, err := client.Connect(
		t.Context(),
		&mcpsdk.StreamableClientTransport{
			Endpoint:             server.URL,
			HTTPClient:           server.Client(),
			DisableStandaloneSSE: true,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("session.Close() error = %v", err)
		}
	})

	return session
}

func hasRequired(schema map[string]any, fields ...string) bool {
	values, ok := schema["required"].([]any)
	if !ok {
		return false
	}

	for _, field := range fields {
		found := false

		for _, value := range values {
			if value == field {
				found = true
				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}

type recordingDiscoverer struct {
	sourcesResult       execution.ListDataSourcesResult
	sourcesErr          error
	waitForCancellation bool
	started             chan struct{}
	cancelled           chan struct{}
	startOnce           sync.Once
	cancelOnce          sync.Once
}

func (d *recordingDiscoverer) ListDataSources(
	ctx context.Context,
	_ execution.ListDataSourcesRequest,
) (execution.ListDataSourcesResult, error) {
	if d.waitForCancellation {
		d.startOnce.Do(func() { close(d.started) })
		<-ctx.Done()
		d.cancelOnce.Do(func() { close(d.cancelled) })

		return execution.ListDataSourcesResult{}, ctx.Err()
	}

	return d.sourcesResult, d.sourcesErr
}

func (d *recordingDiscoverer) ListRelationalSchemas(
	context.Context,
	execution.ListRelationalSchemasRequest,
) (execution.ListRelationalSchemasResult, error) {
	return execution.ListRelationalSchemasResult{}, nil
}

func (d *recordingDiscoverer) ListRelationalTables(
	context.Context,
	execution.ListRelationalTablesRequest,
) (execution.ListRelationalTablesResult, error) {
	return execution.ListRelationalTablesResult{}, nil
}

func (d *recordingDiscoverer) ListRelationalColumns(
	context.Context,
	execution.ListRelationalColumnsRequest,
) (execution.ListRelationalColumnsResult, error) {
	return execution.ListRelationalColumnsResult{}, nil
}
