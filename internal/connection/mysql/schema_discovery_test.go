package mysql

import (
	"context"
	"errors"
	"testing"

	"github.com/adamraziv/dataporch/internal/execution"
)

func TestListSchemasProjectsOnlyImportedDatabase(t *testing.T) {
	t.Parallel()

	pool := &testCatalogPool{}
	opener := &testClientOpener{client: &Client{pool: pool, database: "Finance"}}
	discoverer := lifecycleTestDiscoverer(t, opener)

	tests := []struct {
		name        string
		request     execution.SchemaDiscoveryRequest
		wantSchemas []string
	}{
		{
			name: "empty search",
			request: execution.SchemaDiscoveryRequest{
				SourceID:            "finance",
				IncludeDescriptions: true,
				Limit:               1,
			},
			wantSchemas: []string{"Finance"},
		},
		{
			name: "literal case insensitive search",
			request: execution.SchemaDiscoveryRequest{
				SourceID: "finance",
				Search:   "NAN",
				Limit:    1,
			},
			wantSchemas: []string{"Finance"},
		},
		{
			name: "wildcards remain literal",
			request: execution.SchemaDiscoveryRequest{
				SourceID: "finance",
				Search:   "%_",
				Limit:    1,
			},
		},
		{
			name: "after name",
			request: execution.SchemaDiscoveryRequest{
				SourceID:  "finance",
				AfterName: "Finance",
				Limit:     1,
			},
		},
		{
			name: "zero limit",
			request: execution.SchemaDiscoveryRequest{
				SourceID: "finance",
				Limit:    0,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			page, err := discoverer.ListSchemas(t.Context(), test.request)
			if err != nil {
				t.Fatalf("ListSchemas() error = %v", err)
			}

			if len(page.Schemas) != len(test.wantSchemas) {
				t.Fatalf("schemas = %#v, want names %v", page.Schemas, test.wantSchemas)
			}

			for index, name := range test.wantSchemas {
				if page.Schemas[index].Name != name {
					t.Fatalf("schema[%d].Name = %q, want %q", index, page.Schemas[index].Name, name)
				}

				if page.Schemas[index].Description != nil {
					t.Fatalf("schema[%d].Description = %v, want nil", index, page.Schemas[index].Description)
				}
			}
		})
	}

	pool.mu.Lock()
	queryCount := pool.queryCount
	pool.mu.Unlock()

	if queryCount != 0 {
		t.Fatalf("catalog query count = %d, want 0", queryCount)
	}
}

func TestListSchemasOpensRequestedSource(t *testing.T) {
	t.Parallel()

	opener := &testClientOpener{client: &Client{
		pool:     &testCatalogPool{},
		database: "analytics",
	}}
	discoverer := lifecycleTestDiscoverer(t, opener)

	_, err := discoverer.ListSchemas(t.Context(), execution.SchemaDiscoveryRequest{
		SourceID: "source-1",
		Limit:    1,
	})
	if err != nil {
		t.Fatalf("ListSchemas() error = %v", err)
	}

	opener.mu.Lock()
	sourceID := opener.sourceID
	openCalls := opener.openCall
	opener.mu.Unlock()

	if sourceID != "source-1" || openCalls != 1 {
		t.Fatalf("opener source/calls = %q/%d, want source-1/1", sourceID, openCalls)
	}
}

func TestListSchemasPropagatesContextCancellationBeforeOpen(t *testing.T) {
	t.Parallel()

	opener := &testClientOpener{client: &Client{pool: &testCatalogPool{}, database: "finance"}}
	discoverer := lifecycleTestDiscoverer(t, opener)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := discoverer.ListSchemas(ctx, execution.SchemaDiscoveryRequest{SourceID: "finance", Limit: 1})
	if !errors.Is(err, execution.ErrCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("ListSchemas() error = %v, want cancellation", err)
	}
}
