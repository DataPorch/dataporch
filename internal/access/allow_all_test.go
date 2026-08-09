package access

import (
	"context"
	"testing"
)

func TestAllowAll_Authorize(t *testing.T) {
	t.Parallel()

	policy := NewAllowAll()
	if err := policy.Authorize(t.Context(), ActionListResources); err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
}

func TestAllowAll_AuthorizeRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	policy := NewAllowAll()
	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := policy.Authorize(canceledCtx, ActionListResources); err == nil {
		t.Error("Authorize(canceledCtx) error = nil, want non-nil")
	}

	if err := policy.Authorize(t.Context(), ""); err == nil {
		t.Error("Authorize(empty action) error = nil, want non-nil")
	}
}
