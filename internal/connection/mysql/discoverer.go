package mysql

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	gomysql "github.com/go-sql-driver/mysql"
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
	if err == nil {
		return nil
	}

	if isMySQLDiscoverySentinel(err) {
		return err
	}

	if classification := mysqlDiscoveryContextClassification(parentCtx, queryCtx); classification != nil {
		return wrapMySQLClassification(classification, err)
	}

	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, execution.ErrQueryCancelled):
		return wrapMySQLClassification(execution.ErrCancelled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return wrapMySQLClassification(execution.ErrQueryTimeout, err)
	}

	var mysqlErr *gomysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr != nil {
		return wrapMySQLClassification(mysqlDiscoveryClassification(mysqlErr), err)
	}

	if isMySQLUnavailableError(err) {
		return wrapMySQLClassification(execution.ErrDatabaseUnavailable, err)
	}

	return wrapMySQLClassification(execution.ErrInternal, err)
}

func mysqlDiscoveryContextClassification(parentCtx, queryCtx context.Context) error {
	if parentCtx != nil {
		switch parentCtx.Err() {
		case context.Canceled:
			return execution.ErrCancelled
		case context.DeadlineExceeded:
			return execution.ErrQueryTimeout
		}
	}

	if queryCtx == nil {
		return nil
	}

	switch {
	case errors.Is(context.Cause(queryCtx), execution.ErrQueryTimeout),
		errors.Is(context.Cause(queryCtx), context.DeadlineExceeded):
		return execution.ErrQueryTimeout
	case errors.Is(context.Cause(queryCtx), execution.ErrQueryCancelled),
		errors.Is(context.Cause(queryCtx), context.Canceled):
		return execution.ErrCancelled
	}

	switch queryCtx.Err() {
	case context.DeadlineExceeded:
		return execution.ErrQueryTimeout
	case context.Canceled:
		return execution.ErrCancelled
	default:
		return nil
	}
}

func mysqlDiscoveryClassification(mysqlErr *gomysql.MySQLError) error {
	category, _ := mysqlErrorCategory(mysqlErr.Number, mysqlSQLState(mysqlErr.SQLState))

	if category == execution.ErrorCategoryDatabasePermissionDenied {
		return execution.ErrDatabasePermissionDenied
	}

	if category == execution.ErrorCategoryQueryTimeout {
		return execution.ErrQueryTimeout
	}

	if category == execution.ErrorCategoryQueryCancelled {
		return execution.ErrCancelled
	}

	if category == execution.ErrorCategoryDatabaseUnavailable {
		return execution.ErrDatabaseUnavailable
	}

	return execution.ErrInternal
}

func isMySQLDiscoverySentinel(err error) bool {
	for _, sentinel := range []error{
		execution.ErrSchemaNotFound,
		execution.ErrRelationNotFound,
		execution.ErrUnsupportedRelationKind,
		execution.ErrInternal,
		execution.ErrCancelled,
		execution.ErrQueryTimeout,
		execution.ErrDatabasePermissionDenied,
		execution.ErrDatabaseUnavailable,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}

	return false
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)

	kind := reflected.Kind()
	if kind != reflect.Chan && kind != reflect.Func && kind != reflect.Interface &&
		kind != reflect.Map && kind != reflect.Pointer && kind != reflect.Slice {
		return false
	}

	return reflected.IsNil()
}

var _ execution.RelationalDiscoverer = (*Discoverer)(nil)
