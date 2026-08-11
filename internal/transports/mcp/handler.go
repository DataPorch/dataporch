package mcp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/adamraziv/dataporch/internal/catalog"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxRequestBodyBytes = 1 << 20

var (
	errListerRequired = errors.New("mcp: resource lister is required")
	errLoggerRequired = errors.New("mcp: logger is required")
	errLimitInvalid   = errors.New("mcp: default limit must be positive")
)

type ResourceLister interface {
	ListResources(context.Context, int) ([]catalog.Resource, error)
}

type listResourcesInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum number of resources to return"`
}

type listResourcesOutput struct {
	Resources []catalog.Resource `json:"resources"`
}

func NewResourceHandler(
	lister ResourceLister,
	defaultLimit int,
	logger *slog.Logger,
) (http.Handler, error) {
	if lister == nil {
		return nil, errListerRequired
	}

	if defaultLimit <= 0 {
		return nil, errLimitInvalid
	}

	if logger == nil {
		return nil, errLoggerRequired
	}

	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{
			Name:        "dataporch",
			Title:       "DataPorch",
			Description: "Model-agnostic enterprise data access infrastructure",
			Version:     "dev",
		},
		nil,
	)

	mcpsdk.AddTool(
		server,
		&mcpsdk.Tool{
			Name:        "list_resources",
			Title:       "List resources",
			Description: "List enterprise data resources available through DataPorch",
			Annotations: &mcpsdk.ToolAnnotations{
				DestructiveHint: boolPointer(false),
				IdempotentHint:  true,
				OpenWorldHint:   boolPointer(false),
				ReadOnlyHint:    true,
			},
		},
		func(
			ctx context.Context,
			_ *mcpsdk.CallToolRequest,
			input listResourcesInput,
		) (*mcpsdk.CallToolResult, listResourcesOutput, error) {
			limit := input.Limit
			if limit == 0 {
				limit = defaultLimit
			}

			resources, err := lister.ListResources(ctx, limit)
			if err != nil {
				return nil, listResourcesOutput{}, err
			}

			return nil, listResourcesOutput{Resources: resources}, nil
		},
	)

	streamableHandler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server {
			return server
		},
		&mcpsdk.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			Logger:                       logger,
			MaxRequestBodyBytes:          maxRequestBodyBytes,
			PropagateRequestCancellation: true,
		},
	)

	originProtection := http.NewCrossOriginProtection()

	return originProtection.Handler(streamableHandler), nil
}

func boolPointer(value bool) *bool {
	return &value
}
