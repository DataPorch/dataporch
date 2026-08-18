package mysql

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
	errClientOpenerRequired = errors.New("mysql: client opener is required")
	errQueryTimeoutRequired = errors.New("mysql: query timeout is required")
)

type clientOpener interface {
	Open(context.Context, connection.ID) (*Client, error)
}

type Discoverer struct {
	opener       clientOpener
	queryTimeout time.Duration
}

func NewDiscoverer(opener clientOpener) (*Discoverer, error) {
	return newDiscoverer(opener, metadataQueryTimeout)
}

func newDiscoverer(opener clientOpener, queryTimeout time.Duration) (*Discoverer, error) {
	if isNilInterface(opener) {
		return nil, errClientOpenerRequired
	}
	if queryTimeout <= 0 {
		return nil, errQueryTimeoutRequired
	}

	return &Discoverer{opener: opener, queryTimeout: queryTimeout}, nil
}

func (d *Discoverer) Kind() connection.Kind {
	return Kind
}

func (d *Discoverer) open(ctx context.Context, sourceID connection.ID) (*Client, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", execution.ErrCancelled)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", execution.ErrCancelled, err)
	}

	client, err := d.opener.Open(ctx, sourceID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: %w", execution.ErrCancelled, ctxErr)
		}
		return nil, fmt.Errorf("%w: %w", execution.ErrDatabaseUnavailable, err)
	}
	if client == nil || client.pool == nil {
		return nil, execution.ErrDatabaseUnavailable
	}

	return client, nil
}

func (d *Discoverer) queryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(ctx, d.queryTimeout, execution.ErrQueryTimeout)
}

func classifyDiscoveryQueryError(parentCtx, queryCtx context.Context, err error) error {
	return projectMySQLQueryError(parentCtx, queryCtx, err)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ execution.RelationalDiscoverer = (*Discoverer)(nil)
