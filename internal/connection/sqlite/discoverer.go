package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
)

const metadataQueryTimeout = 20 * time.Second

var (
	errDiscoveryRuntimeRequired     = errors.New("sqlite: discovery runtime is required")
	errMetadataQueryTimeoutRequired = errors.New("sqlite: metadata query timeout is required")
)

type discoveryOpener interface {
	open(context.Context, connection.ID, accessMode) (*client, error)
}

type Discoverer struct {
	runtime      discoveryOpener
	queryTimeout time.Duration
}

func NewDiscoverer(runtime discoveryOpener) (*Discoverer, error) {
	return newDiscoverer(runtime, metadataQueryTimeout)
}

func newDiscoverer(runtime discoveryOpener, queryTimeout time.Duration) (*Discoverer, error) {
	if isNilInterface(runtime) {
		return nil, errDiscoveryRuntimeRequired
	}
	if queryTimeout <= 0 {
		return nil, errMetadataQueryTimeoutRequired
	}

	return &Discoverer{runtime: runtime, queryTimeout: queryTimeout}, nil
}

func (*Discoverer) Kind() connection.Kind {
	return Kind
}

var _ execution.RelationalDiscoverer = (*Discoverer)(nil)

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() { //nolint:exhaustive // Only nil-able kinds can contain typed nil values.
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func (d *Discoverer) open(ctx context.Context, sourceID connection.ID) (*client, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", execution.ErrCancelled)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", execution.ErrCancelled, err)
	}

	client, err := d.runtime.open(ctx, sourceID, accessModeDiscovery)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: %w", execution.ErrCancelled, ctxErr)
		}
		return nil, fmt.Errorf("%w: %w", execution.ErrDatabaseUnavailable, err)
	}
	if client == nil || client.conn == nil {
		return nil, execution.ErrDatabaseUnavailable
	}

	return client, nil
}

func (d *Discoverer) queryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(ctx, d.queryTimeout, execution.ErrQueryTimeout)
}
