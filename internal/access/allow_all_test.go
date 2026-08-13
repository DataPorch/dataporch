package access

import (
	"context"
	"testing"
)

func TestAllowAllAuthorize(t *testing.T) {
	t.Parallel()

	policy := New()
	requests := []Request{
		{Action: ActionListDataSources},
		{Action: ActionListRelationalSchemas, SourceID: "finance"},
		{Action: ActionListRelationalTables, SourceID: "finance"},
		{Action: ActionListRelationalColumns, SourceID: "finance"},
		{Action: ActionQueryRelationalDatabase, Kind: "postgres", SourceID: "finance"},
	}

	for _, request := range requests {
		if err := policy.Authorize(t.Context(), request); err != nil {
			t.Errorf("Authorize(%#v) error = %v", request, err)
		}
	}
}

func TestAllowAllAuthorizeRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	policy := New()
	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := policy.Authorize(canceledCtx, Request{Action: ActionListDataSources}); err == nil {
		t.Error("Authorize(canceledCtx) error = nil, want non-nil")
	}

	if err := policy.Authorize(t.Context(), Request{}); err == nil {
		t.Error("Authorize(empty action) error = nil, want non-nil")
	}
}
