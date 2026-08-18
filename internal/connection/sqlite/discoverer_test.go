package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
)

func TestNewDiscovererValidatesMetadataTimeout(t *testing.T) {
	t.Parallel()

	runtime := &deadlineDiscoveryRuntime{}
	if _, err := newDiscoverer(runtime, 0); !errors.Is(err, errMetadataQueryTimeoutRequired) {
		t.Fatalf("newDiscoverer(timeout 0) error = %v, want timeout validation", err)
	}
}

func TestDiscovererUsesBoundedMetadataContext(t *testing.T) {
	t.Parallel()

	runtime := &deadlineDiscoveryRuntime{}
	timeout := 100 * time.Millisecond
	discoverer, err := newDiscoverer(runtime, timeout)
	if err != nil {
		t.Fatalf("newDiscoverer() error = %v", err)
	}

	page, err := discoverer.ListSchemas(t.Context(), execution.SchemaDiscoveryRequest{
		SourceID: "source",
		Limit:    1,
	})
	if err != nil {
		t.Fatalf("ListSchemas() error = %v", err)
	}
	if len(page.Schemas) != 1 || page.Schemas[0].Name != "main" {
		t.Fatalf("ListSchemas() = %#v, want main schema", page)
	}

	deadline, ok := runtime.context.Deadline()
	remaining := time.Until(deadline)
	if !ok || remaining <= 0 || remaining > timeout {
		t.Fatalf("metadata deadline = %v, remaining %s; want within %s", deadline, remaining, timeout)
	}
}

type deadlineDiscoveryRuntime struct {
	context context.Context
}

func (r *deadlineDiscoveryRuntime) open(ctx context.Context, _ connection.ID, _ accessMode) (*client, error) {
	r.context = ctx
	return &client{
		conn:    &runtimeRawConnection{},
		release: func() {},
	}, nil
}
