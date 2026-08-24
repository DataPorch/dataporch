package main

import (
	"context"
	"errors"
	"testing"
)

func TestExecuteWithCleanupCancelsAfterCommandReturns(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	called := false
	code := executeWithCleanup(ctx, cancel, []string{"--help"}, func(got context.Context, _ []string) int {
		called = true
		if err := got.Err(); err != nil {
			t.Fatalf("command context error = %v before execute returned", err)
		}
		return 7
	})
	if !called {
		t.Fatal("execute callback was not called")
	}
	if code != 7 {
		t.Fatalf("executeWithCleanup() = %d, want 7", code)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want context.Canceled after cleanup", ctx.Err())
	}
}
