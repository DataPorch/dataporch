package mysql

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
)

type fakeDefinitionPreparer struct {
	prepare func(context.Context, connection.ID) (connection.ResolvedDefinition, error)
}

func (f fakeDefinitionPreparer) Prepare(
	ctx context.Context,
	id connection.ID,
) (connection.ResolvedDefinition, error) {
	return f.prepare(ctx, id)
}

type fakePoolFactory struct {
	newPool func(context.Context, connection.ResolvedDefinition) (runtimePool, error)
}

func (f fakePoolFactory) New(
	ctx context.Context,
	definition connection.ResolvedDefinition,
) (runtimePool, error) {
	return f.newPool(ctx, definition)
}

type fakeRuntimePool struct {
	ping  func(context.Context) error
	close func() error
}

func (p *fakeRuntimePool) Ping(ctx context.Context) error {
	if p.ping == nil {
		return nil
	}
	return p.ping(ctx)
}

func (*fakeRuntimePool) Query(context.Context, string, ...any) (catalogRows, error) {
	return nil, errors.New("unexpected query")
}

func (p *fakeRuntimePool) Close() error {
	if p.close == nil {
		return nil
	}
	return p.close()
}

func lifecycleTestOpener(
	t *testing.T,
	preparer DefinitionPreparer,
	pools poolFactory,
) *Opener {
	t.Helper()

	opener, err := newOpener(openerDependencies{
		preparer:    preparer,
		pools:       pools,
		openTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("newOpener() error = %v", err)
	}
	return opener
}

func TestNewOpenerRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()

	validPreparer := fakeDefinitionPreparer{prepare: func(
		context.Context,
		connection.ID,
	) (connection.ResolvedDefinition, error) {
		return validRuntimeDefinition(), nil
	}}
	validFactory := fakePoolFactory{newPool: func(
		context.Context,
		connection.ResolvedDefinition,
	) (runtimePool, error) {
		return &fakeRuntimePool{}, nil
	}}

	tests := []struct {
		name string
		deps openerDependencies
		want error
	}{
		{
			name: "nil preparer",
			deps: openerDependencies{pools: validFactory, openTimeout: time.Second},
			want: errDefinitionPreparerRequired,
		},
		{
			name: "nil pool factory",
			deps: openerDependencies{preparer: validPreparer, openTimeout: time.Second},
			want: errPoolFactoryRequired,
		},
		{
			name: "non positive timeout",
			deps: openerDependencies{preparer: validPreparer, pools: validFactory},
			want: errOpenTimeoutRequired,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := newOpener(test.deps)
			if !errors.Is(err, test.want) {
				t.Fatalf("newOpener() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestOpenerSharesConcurrentOpenAttempt(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	var prepareCalls atomic.Int32
	pool := &fakeRuntimePool{}

	opener := lifecycleTestOpener(
		t,
		fakeDefinitionPreparer{prepare: func(
			ctx context.Context,
			id connection.ID,
		) (connection.ResolvedDefinition, error) {
			if prepareCalls.Add(1) == 1 {
				close(started)
			}
			select {
			case <-release:
				definition := validRuntimeDefinition()
				definition.ID = id
				return definition, nil
			case <-ctx.Done():
				return connection.ResolvedDefinition{}, ctx.Err()
			}
		}},
		fakePoolFactory{newPool: func(
			context.Context,
			connection.ResolvedDefinition,
		) (runtimePool, error) {
			return pool, nil
		}},
	)

	results := make(chan *Client, 2)
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() {
			client, openErr := opener.Open(t.Context(), "finance")
			results <- client
			errorsCh <- openErr
		}()
	}

	<-started
	close(release)

	first := <-results
	second := <-results
	if err := <-errorsCh; err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := <-errorsCh; err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	if first != second {
		t.Fatal("concurrent callers must receive the same cached client")
	}
	if prepareCalls.Load() != 1 {
		t.Fatalf("Prepare() calls = %d, want 1", prepareCalls.Load())
	}
}

func TestOpenerInvalidationRejectsStaleAttempt(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	staleClosed := make(chan struct{})
	stalePool := &fakeRuntimePool{
		ping: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
		close: func() error {
			close(staleClosed)
			return nil
		},
	}
	freshPool := &fakeRuntimePool{}
	var poolCalls atomic.Int32

	opener := lifecycleTestOpener(
		t,
		fakeDefinitionPreparer{prepare: func(
			_ context.Context,
			id connection.ID,
		) (connection.ResolvedDefinition, error) {
			definition := validRuntimeDefinition()
			definition.ID = id
			return definition, nil
		}},
		fakePoolFactory{newPool: func(
			context.Context,
			connection.ResolvedDefinition,
		) (runtimePool, error) {
			if poolCalls.Add(1) == 1 {
				return stalePool, nil
			}
			return freshPool, nil
		}},
	)

	staleDone := make(chan error, 1)
	go func() {
		_, openErr := opener.Open(t.Context(), "finance")
		staleDone <- openErr
	}()

	<-started
	opener.Invalidate("finance")
	close(release)

	if err := <-staleDone; !errors.Is(err, errOpenInvalidated) {
		t.Fatalf("stale Open() error = %v, want %v", err, errOpenInvalidated)
	}
	<-staleClosed

	client, err := opener.Open(t.Context(), "finance")
	if err != nil {
		t.Fatalf("fresh Open() error = %v", err)
	}
	if client.pool != freshPool {
		t.Fatal("fresh generation did not publish the new pool")
	}
	if poolCalls.Load() != 2 {
		t.Fatalf("pool factory calls = %d, want 2", poolCalls.Load())
	}
}

func TestOpenerRemainingLifecycleBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("nil context", func(t *testing.T) {
		var prepares atomic.Int32
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(context.Context, connection.ID) (connection.ResolvedDefinition, error) {
				prepares.Add(1)
				return validRuntimeDefinition(), nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) {
				return &fakeRuntimePool{}, nil
			}},
		)

		_, err := opener.Open(nil, "finance")
		if !errors.Is(err, errRuntimeContextRequired) || prepares.Load() != 0 {
			t.Fatalf("Open(nil) error=%v prepares=%d", err, prepares.Load())
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var prepares atomic.Int32
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(context.Context, connection.ID) (connection.ResolvedDefinition, error) {
				prepares.Add(1)
				return validRuntimeDefinition(), nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) {
				return &fakeRuntimePool{}, nil
			}},
		)

		_, err := opener.Open(ctx, "finance")
		if !errors.Is(err, context.Canceled) || prepares.Load() != 0 {
			t.Fatalf("Open(cancelled) error=%v prepares=%d", err, prepares.Load())
		}
	})

	t.Run("invalid id before prepare", func(t *testing.T) {
		for _, id := range []connection.ID{"", "not valid", "finance/reporting"} {
			var prepares atomic.Int32
			opener := lifecycleTestOpener(t,
				fakeDefinitionPreparer{prepare: func(context.Context, connection.ID) (connection.ResolvedDefinition, error) {
					prepares.Add(1)
					return validRuntimeDefinition(), nil
				}},
				fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) {
					return &fakeRuntimePool{}, nil
				}},
			)
			_, err := opener.Open(t.Context(), id)
			if !errors.Is(err, errRuntimeInvalidID) || prepares.Load() != 0 {
				t.Fatalf("Open(%q) error=%v prepares=%d", id, err, prepares.Load())
			}
		}
	})

	t.Run("resolved id mismatch", func(t *testing.T) {
		var factories atomic.Int32
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(context.Context, connection.ID) (connection.ResolvedDefinition, error) {
				definition := validRuntimeDefinition()
				definition.ID = "other"
				return definition, nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) {
				factories.Add(1)
				return &fakeRuntimePool{}, nil
			}},
		)
		_, err := opener.Open(t.Context(), "finance")
		if !errors.Is(err, errRuntimeDefinitionMismatch) || factories.Load() != 0 {
			t.Fatalf("Open() error=%v factories=%d", err, factories.Load())
		}
	})

	t.Run("resolved kind mismatch", func(t *testing.T) {
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(context.Context, connection.ID) (connection.ResolvedDefinition, error) {
				definition := validRuntimeDefinition()
				definition.Kind = "postgres"
				return definition, nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) {
				return &fakeRuntimePool{}, nil
			}},
		)
		_, err := opener.Open(t.Context(), "finance")
		if !errors.Is(err, ErrUnsupportedKind) {
			t.Fatalf("Open() error=%v, want %v", err, ErrUnsupportedKind)
		}
	})

	t.Run("factory failure is not cached", func(t *testing.T) {
		factoryErr := errors.New("factory failed")
		var prepares atomic.Int32
		var factories atomic.Int32
		pool := &fakeRuntimePool{}
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(_ context.Context, id connection.ID) (connection.ResolvedDefinition, error) {
				prepares.Add(1)
				definition := validRuntimeDefinition()
				definition.ID = id
				return definition, nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) {
				if factories.Add(1) == 1 {
					return nil, factoryErr
				}
				return pool, nil
			}},
		)
		if _, err := opener.Open(t.Context(), "finance"); !errors.Is(err, factoryErr) {
			t.Fatalf("first Open() error=%v, want %v", err, factoryErr)
		}
		if _, err := opener.Open(t.Context(), "finance"); err != nil {
			t.Fatalf("second Open() error=%v", err)
		}
		if prepares.Load() != 2 || factories.Load() != 2 {
			t.Fatalf("prepares=%d factories=%d, want 2/2", prepares.Load(), factories.Load())
		}
	})

	t.Run("ping failure is not cached", func(t *testing.T) {
		pingErr := errors.New("ping failed")
		failedClosed := make(chan struct{})
		failed := &fakeRuntimePool{
			ping: func(context.Context) error { return pingErr },
			close: func() error {
				close(failedClosed)
				return nil
			},
		}
		fresh := &fakeRuntimePool{}
		var factories atomic.Int32
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(_ context.Context, id connection.ID) (connection.ResolvedDefinition, error) {
				definition := validRuntimeDefinition()
				definition.ID = id
				return definition, nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) {
				if factories.Add(1) == 1 {
					return failed, nil
				}
				return fresh, nil
			}},
		)
		if _, err := opener.Open(t.Context(), "finance"); !errors.Is(err, pingErr) {
			t.Fatalf("first Open() error=%v, want %v", err, pingErr)
		}
		<-failedClosed
		client, err := opener.Open(t.Context(), "finance")
		if err != nil || client.pool != fresh || factories.Load() != 2 {
			t.Fatalf("second Open() client=%#v error=%v factories=%d", client, err, factories.Load())
		}
	})

	t.Run("successful pool is cached", func(t *testing.T) {
		var prepares atomic.Int32
		var factories atomic.Int32
		var pings atomic.Int32
		pool := &fakeRuntimePool{ping: func(context.Context) error { pings.Add(1); return nil }}
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(_ context.Context, id connection.ID) (connection.ResolvedDefinition, error) {
				prepares.Add(1)
				definition := validRuntimeDefinition()
				definition.ID = id
				return definition, nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) {
				factories.Add(1)
				return pool, nil
			}},
		)
		first, err := opener.Open(t.Context(), "finance")
		if err != nil {
			t.Fatalf("first Open() error=%v", err)
		}
		second, err := opener.Open(t.Context(), "finance")
		if err != nil {
			t.Fatalf("second Open() error=%v", err)
		}
		if first != second || first.database != "finance" || prepares.Load() != 1 || factories.Load() != 1 || pings.Load() != 1 {
			t.Fatalf("cache mismatch first=%#v second=%#v prepares=%d factories=%d pings=%d", first, second, prepares.Load(), factories.Load(), pings.Load())
		}
	})

	t.Run("close is idempotent and retains close error", func(t *testing.T) {
		closeErr := errors.New("close failed")
		var closes atomic.Int32
		pool := &fakeRuntimePool{close: func() error { closes.Add(1); return closeErr }}
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(_ context.Context, id connection.ID) (connection.ResolvedDefinition, error) {
				definition := validRuntimeDefinition()
				definition.ID = id
				return definition, nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) { return pool, nil }},
		)
		if _, err := opener.Open(t.Context(), "finance"); err != nil {
			t.Fatalf("Open() error=%v", err)
		}
		if err := opener.Close(t.Context()); !errors.Is(err, closeErr) {
			t.Fatalf("first Close() error=%v", err)
		}
		if err := opener.Close(t.Context()); !errors.Is(err, closeErr) {
			t.Fatalf("second Close() error=%v", err)
		}
		if closes.Load() != 1 {
			t.Fatalf("pool closes=%d, want 1", closes.Load())
		}
	})

	t.Run("close rejects later open", func(t *testing.T) {
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(context.Context, connection.ID) (connection.ResolvedDefinition, error) {
				return validRuntimeDefinition(), nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) {
				return &fakeRuntimePool{}, nil
			}},
		)
		if err := opener.Close(t.Context()); err != nil {
			t.Fatalf("Close() error=%v", err)
		}
		if _, err := opener.Open(t.Context(), "finance"); !errors.Is(err, ErrRuntimeClosed) {
			t.Fatalf("Open() after Close error=%v, want %v", err, ErrRuntimeClosed)
		}
	})

	t.Run("close waits for tracked pool close", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		pool := &fakeRuntimePool{close: func() error {
			close(started)
			<-release
			return nil
		}}
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(_ context.Context, id connection.ID) (connection.ResolvedDefinition, error) {
				definition := validRuntimeDefinition()
				definition.ID = id
				return definition, nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) { return pool, nil }},
		)
		if _, err := opener.Open(t.Context(), "finance"); err != nil {
			t.Fatalf("Open() error=%v", err)
		}
		done := make(chan error, 1)
		go func() { done <- opener.Close(t.Context()) }()
		<-started
		select {
		case err := <-done:
			t.Fatalf("Close() returned before tracked close finished: %v", err)
		default:
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("Close() error=%v", err)
		}
	})

	t.Run("close joins multiple pool errors", func(t *testing.T) {
		firstErr := errors.New("first close")
		secondErr := errors.New("second close")
		var factories atomic.Int32
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(_ context.Context, id connection.ID) (connection.ResolvedDefinition, error) {
				definition := validRuntimeDefinition()
				definition.ID = id
				return definition, nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) {
				if factories.Add(1) == 1 {
					return &fakeRuntimePool{close: func() error { return firstErr }}, nil
				}
				return &fakeRuntimePool{close: func() error { return secondErr }}, nil
			}},
		)
		for _, id := range []connection.ID{"finance", "reporting"} {
			if _, err := opener.Open(t.Context(), id); err != nil {
				t.Fatalf("Open(%q) error=%v", id, err)
			}
		}
		err := opener.Close(t.Context())
		if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
			t.Fatalf("Close() error=%v, want both close errors", err)
		}
	})

	t.Run("resolved secrets are cleared", func(t *testing.T) {
		password := []byte("secret")
		opener := lifecycleTestOpener(t,
			fakeDefinitionPreparer{prepare: func(_ context.Context, id connection.ID) (connection.ResolvedDefinition, error) {
				definition := validRuntimeDefinition()
				definition.ID = id
				definition.Secrets[settingPassword] = password
				return definition, nil
			}},
			fakePoolFactory{newPool: func(context.Context, connection.ResolvedDefinition) (runtimePool, error) {
				return &fakeRuntimePool{}, nil
			}},
		)
		if _, err := opener.Open(t.Context(), "finance"); err != nil {
			t.Fatalf("Open() error=%v", err)
		}
		for index, value := range password {
			if value != 0 {
				t.Fatalf("password[%d]=%d, want zero", index, value)
			}
		}
	})
}
