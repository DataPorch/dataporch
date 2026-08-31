package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/DataPorch/dataporch/internal/connection"
	"github.com/DataPorch/dataporch/internal/execution"
	"github.com/DataPorch/dataporch/internal/secret"
)

func TestNewRelationalCompositionProjectsModulesInFactoryOrder(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)
	alpha := newRelationalTestModule("alpha", &relationalRuntimeStub{name: "alpha"})
	beta := newRelationalTestModule("beta", &relationalRuntimeStub{name: "beta"})

	composition, err := newRelationalComposition(manager, relationalCompositionOptions{
		factories: []relationalModuleFactory{
			func(*connection.Manager, queryPolicy) (relationalModule, error) { return alpha, nil },
			func(*connection.Manager, queryPolicy) (relationalModule, error) { return beta, nil },
		},
		policy:        validQueryPolicy(),
		cleanupPeriod: time.Second,
	})
	if err != nil {
		t.Fatalf("newRelationalComposition() error = %v", err)
	}

	if got := []connection.Kind{
		composition.adapters[0].Kind(),
		composition.adapters[1].Kind(),
	}; !slices.Equal(got, []connection.Kind{"alpha", "beta"}) {
		t.Fatalf("adapter kinds = %v, want [alpha beta]", got)
	}

	if composition.discoverers[0].Kind() != "alpha" || composition.queryExecutors[1].Kind() != "beta" {
		t.Fatal("discoverer/query projections lost factory order")
	}

	if composition.runtimeByKind["alpha"] != alpha.runtime || composition.runtimeByKind["beta"] != beta.runtime {
		t.Fatal("runtime ownership index does not match modules")
	}
}

func TestValidateRelationalModuleRejectsInvalidComponents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*relationalModule)
		want   error
	}{
		{name: "nil adapter", mutate: func(module *relationalModule) { module.adapter = nil }, want: errRelationalAdapterRequired},
		{name: "typed nil adapter", mutate: func(module *relationalModule) { module.adapter = (*relationalAdapterStub)(nil) }, want: errRelationalAdapterRequired},
		{name: "nil discoverer", mutate: func(module *relationalModule) { module.discoverer = nil }, want: errRelationalDiscovererRequired},
		{name: "typed nil discoverer", mutate: func(module *relationalModule) { module.discoverer = (*relationalDiscovererStub)(nil) }, want: errRelationalDiscovererRequired},
		{name: "nil query executor", mutate: func(module *relationalModule) { module.queryExecutor = nil }, want: errRelationalQueryExecutorRequired},
		{name: "typed nil query executor", mutate: func(module *relationalModule) { module.queryExecutor = (*relationalQueryExecutorStub)(nil) }, want: errRelationalQueryExecutorRequired},
		{name: "nil runtime", mutate: func(module *relationalModule) { module.runtime = nil }, want: errRelationalRuntimeRequired},
		{name: "typed nil runtime", mutate: func(module *relationalModule) { module.runtime = (*relationalRuntimeStub)(nil) }, want: errRelationalRuntimeRequired},
		{name: "empty kind", mutate: func(module *relationalModule) { module.adapter = &relationalAdapterStub{} }, want: errRelationalKindRequired},
		{name: "discoverer kind mismatch", mutate: func(module *relationalModule) { module.discoverer = &relationalDiscovererStub{kind: "beta"} }, want: errRelationalDiscovererKindMismatch},
		{name: "query executor kind mismatch", mutate: func(module *relationalModule) { module.queryExecutor = &relationalQueryExecutorStub{kind: "beta"} }, want: errRelationalQueryExecutorKindMismatch},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			module := newRelationalTestModule("alpha", &relationalRuntimeStub{name: "alpha"})
			test.mutate(&module)

			_, err := validateRelationalModule(module)
			if !errors.Is(err, test.want) {
				t.Fatalf("validateRelationalModule() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNewRelationalCompositionRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	validFactory := func(*connection.Manager, queryPolicy) (relationalModule, error) {
		return newRelationalTestModule("alpha", &relationalRuntimeStub{name: "alpha"}), nil
	}

	tests := []struct {
		name      string
		manager   *connection.Manager
		factories []relationalModuleFactory
		period    time.Duration
		want      error
	}{
		{name: "nil manager", factories: []relationalModuleFactory{validFactory}, period: time.Second, want: errRelationalManagerRequired},
		{name: "invalid cleanup period", manager: newRelationalTestManager(t), factories: []relationalModuleFactory{validFactory}, period: 0, want: errRelationalCleanupPeriodInvalid},
		{name: "nil factory", manager: newRelationalTestManager(t), factories: []relationalModuleFactory{nil}, period: time.Second, want: errRelationalFactoryRequired},
		{name: "duplicate kind", manager: newRelationalTestManager(t), factories: []relationalModuleFactory{validFactory, validFactory}, period: time.Second, want: errRelationalDuplicateKind},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := newRelationalComposition(test.manager, relationalCompositionOptions{
				factories:     test.factories,
				policy:        validQueryPolicy(),
				cleanupPeriod: test.period,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("newRelationalComposition() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCloseRuntimeLifecyclesClosesEveryRuntimeAndPreservesErrorOrder(t *testing.T) {
	t.Parallel()

	firstErr := errors.New("first close failed")
	secondErr := errors.New("second close failed")
	first := &relationalRuntimeStub{name: "first", closeErr: firstErr}
	second := &relationalRuntimeStub{name: "second", closeErr: secondErr}
	third := &relationalRuntimeStub{name: "third"}

	err := closeRuntimeLifecycles(t.Context(), []runtimeLifecycle{
		first,
		second,
		third,
	})

	if got, want := err.Error(), "first close failed\nsecond close failed"; got != want {
		t.Fatalf("close error = %q, want %q", got, want)
	}

	for _, runtime := range []*relationalRuntimeStub{first, second, third} {
		if got := relationalRuntimeCloseCalls(runtime); got != 1 {
			t.Fatalf("%s runtime close calls = %d, want 1", runtime.name, got)
		}
	}

	for _, expected := range []error{firstErr, secondErr} {
		if !errors.Is(err, expected) {
			t.Errorf("close error = %v, want %v", err, expected)
		}
	}
}

func TestCloseRuntimeLifecyclesStartsEveryRuntimeWithLiveContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	second := &contextObservingRuntimeStub{started: make(chan struct{})}

	done := make(chan error, 1)
	go func() {
		done <- closeRuntimeLifecycles(ctx, []runtimeLifecycle{
			&blockingRuntimeStub{},
			second,
		})
	}()

	select {
	case <-second.started:
		if second.closeContextErr != nil {
			t.Fatalf("second runtime close context error = %v, want nil", second.closeContextErr)
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("second runtime close did not start before the shared context was canceled")
	}

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("closeRuntimeLifecycles() error = %v, want nil", err)
	}
}

func TestNewRelationalCompositionCleansCreatedRuntimesAfterFactoryFailure(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)
	closeErr := errors.New("close failed")
	factoryErr := errors.New("factory failed")
	firstRuntime := &relationalRuntimeStub{name: "first", closeErr: closeErr}

	_, err := newRelationalComposition(manager, relationalCompositionOptions{
		factories: []relationalModuleFactory{
			func(*connection.Manager, queryPolicy) (relationalModule, error) {
				return newRelationalTestModule("alpha", firstRuntime), nil
			},
			func(*connection.Manager, queryPolicy) (relationalModule, error) {
				return relationalModule{}, factoryErr
			},
		},
		policy:        validQueryPolicy(),
		cleanupPeriod: time.Second,
	})
	if !errors.Is(err, factoryErr) {
		t.Fatalf("newRelationalComposition() error = %v, want factory error", err)
	}

	if !errors.Is(err, closeErr) {
		t.Fatalf("newRelationalComposition() error = %v, want close error", err)
	}

	if got := relationalRuntimeCloseCalls(firstRuntime); got != 1 {
		t.Fatalf("first runtime close calls = %d, want 1", got)
	}
}

func TestNewRelationalCompositionCleansCurrentRuntimeAfterValidationFailure(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)
	firstRuntime := &relationalRuntimeStub{name: "first"}
	currentRuntime := &relationalRuntimeStub{name: "current"}

	_, err := newRelationalComposition(manager, relationalCompositionOptions{
		factories: []relationalModuleFactory{
			func(*connection.Manager, queryPolicy) (relationalModule, error) {
				return newRelationalTestModule("alpha", firstRuntime), nil
			},
			func(*connection.Manager, queryPolicy) (relationalModule, error) {
				module := newRelationalTestModule("beta", currentRuntime)
				module.queryExecutor = &relationalQueryExecutorStub{kind: "gamma"}

				return module, nil
			},
		},
		policy:        validQueryPolicy(),
		cleanupPeriod: time.Second,
	})
	if !errors.Is(err, errRelationalQueryExecutorKindMismatch) {
		t.Fatalf("newRelationalComposition() error = %v, want query executor mismatch", err)
	}

	if got := relationalRuntimeCloseCalls(firstRuntime); got != 1 {
		t.Fatalf("first runtime close calls = %d, want 1", got)
	}

	if got := relationalRuntimeCloseCalls(currentRuntime); got != 1 {
		t.Fatalf("current runtime close calls = %d, want 1", got)
	}
}

type relationalAdapterStub struct {
	kind connection.Kind
}

func (a *relationalAdapterStub) Kind() connection.Kind { return a.kind }

func (*relationalAdapterStub) ParseConnectionString([]byte) (connection.ParsedConnection, error) {
	return connection.ParsedConnection{
		Settings: make(map[string]string),
		Secrets:  make(map[string][]byte),
	}, nil
}

type relationalDiscovererStub struct {
	kind connection.Kind
}

func (d *relationalDiscovererStub) Kind() connection.Kind { return d.kind }

func (d *relationalDiscovererStub) ListSchemas(
	context.Context,
	execution.SchemaDiscoveryRequest,
) (execution.SchemaDiscoveryPage, error) {
	return execution.SchemaDiscoveryPage{
		Schemas: []execution.Schema{{Name: string(d.kind)}},
	}, nil
}

func (*relationalDiscovererStub) ListTables(
	context.Context,
	execution.TableDiscoveryRequest,
) (execution.TableDiscoveryPage, error) {
	return execution.TableDiscoveryPage{Tables: []execution.Table{}}, nil
}

func (*relationalDiscovererStub) ListColumns(
	context.Context,
	execution.ColumnDiscoveryRequest,
) (execution.ColumnDiscoveryPage, error) {
	return execution.ColumnDiscoveryPage{
		Columns:     []execution.Column{},
		Constraints: []execution.Constraint{},
	}, nil
}

type relationalQueryExecutorStub struct {
	kind connection.Kind
}

func (e *relationalQueryExecutorStub) Kind() connection.Kind { return e.kind }

func (e *relationalQueryExecutorStub) Query(
	_ context.Context,
	request execution.RelationalQueryExecutionRequest,
) (execution.RelationalQueryResult, error) {
	return execution.RelationalQueryResult{
		Kind:     e.kind,
		SourceID: request.Source.ID,
		Columns:  []execution.RelationalQueryColumn{},
		Rows:     [][]*string{},
	}, nil
}

type relationalRuntimeStub struct {
	mu            sync.Mutex
	name          string
	events        *[]string
	closeErr      error
	closeCalls    int
	invalidations []connection.ID
}

type blockingRuntimeStub struct{}

func (*blockingRuntimeStub) Invalidate(connection.ID) {}

func (*blockingRuntimeStub) Close(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

type contextObservingRuntimeStub struct {
	started         chan struct{}
	closeContextErr error
}

func (*contextObservingRuntimeStub) Invalidate(connection.ID) {}

func (r *contextObservingRuntimeStub) Close(ctx context.Context) error {
	r.closeContextErr = ctx.Err()
	close(r.started)

	return nil
}

func (r *relationalRuntimeStub) Invalidate(id connection.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.invalidations = append(r.invalidations, id)
	if r.events != nil {
		*r.events = append(*r.events, r.name+":"+string(id))
	}
}

func (r *relationalRuntimeStub) Close(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.closeCalls++
	if r.events != nil {
		*r.events = append(*r.events, r.name)
	}

	return r.closeErr
}

func relationalRuntimeCloseCalls(runtime *relationalRuntimeStub) int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	return runtime.closeCalls
}

type relationalResolverStub struct{}

func (relationalResolverStub) Resolve(context.Context, secret.Reference) ([]byte, error) {
	return []byte("resolved"), nil
}

func newRelationalTestManager(t *testing.T) *connection.Manager {
	t.Helper()

	manager, err := connection.NewManager(relationalResolverStub{}, nil)
	if err != nil {
		t.Fatalf("connection.NewManager() error = %v", err)
	}

	return manager
}

func validQueryPolicy() queryPolicy {
	return queryPolicy{
		timeout:           time.Second,
		responseByteLimit: 1024,
		truncationEnabled: true,
		rowLimit:          100,
	}
}

func newRelationalTestModule(
	kind connection.Kind,
	runtime runtimeLifecycle,
) relationalModule {
	return relationalModule{
		adapter:       &relationalAdapterStub{kind: kind},
		discoverer:    &relationalDiscovererStub{kind: kind},
		queryExecutor: &relationalQueryExecutorStub{kind: kind},
		runtime:       runtime,
	}
}
