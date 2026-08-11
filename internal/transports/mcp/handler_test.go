package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/adamraziv/dataporch/internal/catalog"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type listerStub struct {
	resources []catalog.Resource
}

func (s listerStub) ListResources(
	_ context.Context,
	limit int,
) ([]catalog.Resource, error) {
	return s.resources[:min(limit, len(s.resources))], nil
}

func TestHandler_ListResources(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	handler, err := NewResourceHandler(
		listerStub{resources: []catalog.Resource{
			{URI: "memory://customers", Name: "Customers", Kind: "table"},
		}},
		10,
		logger,
	)
	if err != nil {
		t.Fatalf("NewResourceHandler() error = %v", err)
	}

	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "dataporch-test", Version: "dev"},
		nil,
	)

	session, err := client.Connect(
		t.Context(),
		&mcpsdk.StreamableClientTransport{
			Endpoint:             httpServer.URL,
			HTTPClient:           httpServer.Client(),
			DisableStandaloneSSE: true,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	result, err := session.CallTool(
		t.Context(),
		&mcpsdk.CallToolParams{
			Name:      "list_resources",
			Arguments: listResourcesInput{Limit: 1},
		},
	)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}

	if result.IsError {
		t.Fatalf("CallTool() returned tool error: %v", result.Content)
	}

	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling structured content: %v", err)
	}

	var output listResourcesOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("unmarshaling structured content: %v", err)
	}

	if len(output.Resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(output.Resources))
	}
}
