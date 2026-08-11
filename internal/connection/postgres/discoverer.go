package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	"github.com/jackc/pgx/v5/pgconn"
)

const metadataQueryTimeout = 20 * time.Second

var (
	errClientOpenerRequired = errors.New("postgres: client opener is required")
	errQueryTimeoutRequired = errors.New("postgres: query timeout is required")
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
	if isNilClientOpener(opener) {
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

//nolint:gocyclo // Query errors retain distinct cancellation, timeout, permission, and availability categories.
func classifyQueryError(parentCtx, queryCtx context.Context, err error) error {
	if parentCtx != nil {
		if ctxErr := parentCtx.Err(); ctxErr != nil {
			return fmt.Errorf("%w: %w", execution.ErrCancelled, ctxErr)
		}
	}

	if queryCtx != nil {
		if cause := context.Cause(queryCtx); cause != nil {
			switch {
			case errors.Is(cause, execution.ErrQueryTimeout):
				return fmt.Errorf("%w: %w", execution.ErrQueryTimeout, err)
			case errors.Is(cause, context.Canceled):
				return fmt.Errorf("%w: %w", execution.ErrCancelled, cause)
			}
		}
	}

	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", execution.ErrCancelled, err)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", execution.ErrQueryTimeout, err)
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "57014":
			return fmt.Errorf("%w: %w", execution.ErrQueryTimeout, err)
		case pgErr.Code == "42501":
			return fmt.Errorf("%w: %w", execution.ErrDatabasePermissionDenied, err)
		case strings.HasPrefix(pgErr.Code, "08"):
			return fmt.Errorf("%w: %w", execution.ErrDatabaseUnavailable, err)
		}
	}

	if pgconn.SafeToRetry(err) {
		return fmt.Errorf("%w: %w", execution.ErrDatabaseUnavailable, err)
	}

	return fmt.Errorf("%w: %w", execution.ErrInternal, err)
}

func isNilClientOpener(opener clientOpener) bool {
	if opener == nil {
		return true
	}

	value := reflect.ValueOf(opener)
	switch value.Kind() { //nolint:exhaustive // Other kinds cannot be nil.
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ execution.RelationalDiscoverer = (*Discoverer)(nil)
