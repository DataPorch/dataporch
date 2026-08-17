package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
)

var (
	errRelationalManagerRequired           = errors.New("relational manager is required")
	errRelationalCleanupPeriodInvalid      = errors.New("relational cleanup period must be positive")
	errRelationalFactoryRequired           = errors.New("relational module factory is required")
	errRelationalAdapterRequired           = errors.New("relational adapter is required")
	errRelationalDiscovererRequired        = errors.New("relational discoverer is required")
	errRelationalQueryExecutorRequired     = errors.New("relational query executor is required")
	errRelationalRuntimeRequired           = errors.New("relational runtime is required")
	errRelationalKindRequired              = errors.New("relational adapter kind is required")
	errRelationalDiscovererKindMismatch    = errors.New("relational discoverer kind mismatch")
	errRelationalQueryExecutorKindMismatch = errors.New("relational query executor kind mismatch")
	errRelationalDuplicateKind             = errors.New("duplicate relational module kind")
)

type runtimeLifecycle interface {
	Invalidate(connection.ID)
	Close(context.Context) error
}

type queryPolicy struct {
	timeout           time.Duration
	responseByteLimit int
	truncationEnabled bool
	rowLimit          int
}

type relationalModule struct {
	adapter       connection.Adapter
	discoverer    execution.RelationalDiscoverer
	queryExecutor execution.RelationalQueryExecutor
	runtime       runtimeLifecycle
}

type relationalModuleFactory func(
	*connection.Manager,
	queryPolicy,
) (relationalModule, error)

type relationalComposition struct {
	adapters       []connection.Adapter
	discoverers    []execution.RelationalDiscoverer
	queryExecutors []execution.RelationalQueryExecutor
	runtimes       []runtimeLifecycle
	runtimeByKind  map[connection.Kind]runtimeLifecycle
}

type relationalCompositionOptions struct {
	factories     []relationalModuleFactory
	policy        queryPolicy
	cleanupPeriod time.Duration
}

func newRelationalComposition(
	manager *connection.Manager,
	options relationalCompositionOptions,
) (relationalComposition, error) {
	if manager == nil {
		return relationalComposition{}, errRelationalManagerRequired
	}

	if options.cleanupPeriod <= 0 {
		return relationalComposition{}, errRelationalCleanupPeriodInvalid
	}

	composition := relationalComposition{
		adapters:       make([]connection.Adapter, 0, len(options.factories)),
		discoverers:    make([]execution.RelationalDiscoverer, 0, len(options.factories)),
		queryExecutors: make([]execution.RelationalQueryExecutor, 0, len(options.factories)),
		runtimes:       make([]runtimeLifecycle, 0, len(options.factories)),
		runtimeByKind:  make(map[connection.Kind]runtimeLifecycle, len(options.factories)),
	}

	for index, factory := range options.factories {
		if factory == nil {
			return relationalComposition{}, joinRuntimeCleanup(
				fmt.Errorf("factory %d: %w", index, errRelationalFactoryRequired),
				options.cleanupPeriod,
				composition.runtimes,
			)
		}

		module, err := factory(manager, options.policy)
		if err != nil {
			return relationalComposition{}, joinRuntimeCleanup(
				fmt.Errorf("creating relational module %d: %w", index, err),
				options.cleanupPeriod,
				composition.runtimes,
			)
		}

		cleanupRuntimes := slices.Clone(composition.runtimes)
		if !isNilDependency(module.runtime) {
			cleanupRuntimes = append(cleanupRuntimes, module.runtime)
		}

		kind, err := validateRelationalModule(module)
		if err != nil {
			return relationalComposition{}, joinRuntimeCleanup(
				fmt.Errorf("validating relational module %d: %w", index, err),
				options.cleanupPeriod,
				cleanupRuntimes,
			)
		}

		if _, exists := composition.runtimeByKind[kind]; exists {
			return relationalComposition{}, joinRuntimeCleanup(
				fmt.Errorf("%w: %q", errRelationalDuplicateKind, kind),
				options.cleanupPeriod,
				cleanupRuntimes,
			)
		}

		composition.adapters = append(composition.adapters, module.adapter)
		composition.discoverers = append(composition.discoverers, module.discoverer)
		composition.queryExecutors = append(composition.queryExecutors, module.queryExecutor)
		composition.runtimes = append(composition.runtimes, module.runtime)
		composition.runtimeByKind[kind] = module.runtime
	}

	return composition, nil
}

func validateRelationalModule(module relationalModule) (connection.Kind, error) {
	if isNilDependency(module.adapter) {
		return "", errRelationalAdapterRequired
	}

	if isNilDependency(module.discoverer) {
		return "", errRelationalDiscovererRequired
	}

	if isNilDependency(module.queryExecutor) {
		return "", errRelationalQueryExecutorRequired
	}

	if isNilDependency(module.runtime) {
		return "", errRelationalRuntimeRequired
	}

	kind := module.adapter.Kind()
	if kind == "" {
		return "", errRelationalKindRequired
	}

	if discovererKind := module.discoverer.Kind(); discovererKind != kind {
		return "", fmt.Errorf(
			"%w: adapter=%q discoverer=%q",
			errRelationalDiscovererKindMismatch,
			kind,
			discovererKind,
		)
	}

	if queryKind := module.queryExecutor.Kind(); queryKind != kind {
		return "", fmt.Errorf(
			"%w: adapter=%q query_executor=%q",
			errRelationalQueryExecutorKindMismatch,
			kind,
			queryKind,
		)
	}

	return kind, nil
}

func isNilDependency(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)

	kind := reflected.Kind()
	if kind != reflect.Chan &&
		kind != reflect.Func &&
		kind != reflect.Interface &&
		kind != reflect.Map &&
		kind != reflect.Pointer &&
		kind != reflect.Slice {
		return false
	}

	return reflected.IsNil()
}

func joinRuntimeCleanup(
	cause error,
	cleanupPeriod time.Duration,
	runtimes []runtimeLifecycle,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupPeriod)
	defer cancel()

	return errors.Join(cause, closeRuntimeLifecycles(ctx, runtimes))
}

func closeRuntimeLifecycles(ctx context.Context, runtimes []runtimeLifecycle) error {
	errs := make([]error, 0, len(runtimes))
	for _, runtime := range runtimes {
		if isNilDependency(runtime) {
			continue
		}

		if err := runtime.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
