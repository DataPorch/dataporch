package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
)

var (
	errQueryOpenerRequired    = errors.New("sqlite: query opener is required")
	errQueryTimeoutRequired   = errors.New("sqlite: query timeout is required")
	errQueryByteLimitRequired = errors.New("sqlite: query byte limit is required")
	errQueryRowLimitRequired  = errors.New("sqlite: query row limit is required")
)

type QueryOptions struct {
	Timeout           time.Duration
	ResponseByteLimit int
	TruncationEnabled bool
	RowLimit          int
}

type queryOpener interface {
	open(context.Context, connection.ID, accessMode) (*client, error)
}

type QueryExecutor struct {
	runtime   queryOpener
	timeout   time.Duration
	byteLimit int
	truncate  bool
	rowLimit  int
}

var _ execution.RelationalQueryExecutor = (*QueryExecutor)(nil)

func NewQueryExecutor(runtime queryOpener, options QueryOptions) (*QueryExecutor, error) {
	if isNilInterface(runtime) {
		return nil, errQueryOpenerRequired
	}
	if options.Timeout <= 0 {
		return nil, errQueryTimeoutRequired
	}
	if options.ResponseByteLimit <= 0 {
		return nil, errQueryByteLimitRequired
	}
	if options.TruncationEnabled && options.RowLimit <= 0 {
		return nil, errQueryRowLimitRequired
	}
	return &QueryExecutor{
		runtime:   runtime,
		timeout:   options.Timeout,
		byteLimit: options.ResponseByteLimit,
		truncate:  options.TruncationEnabled,
		rowLimit:  options.RowLimit,
	}, nil
}

func (e *QueryExecutor) Kind() connection.Kind {
	return Kind
}

func (e *QueryExecutor) Query(
	requestContext context.Context,
	request execution.RelationalQueryExecutionRequest,
) (result execution.RelationalQueryResult, returnErr error) {
	if requestContext == nil {
		return result, execution.ErrCancelled
	}
	if err := requestContext.Err(); err != nil {
		return result, queryContextError(requestContext, nil, err)
	}
	if request.Source.ID == "" || request.Source.Kind != Kind {
		return result, execution.ErrInvalidRequest
	}
	if strings.TrimSpace(request.Query) == "" {
		return result, execution.ErrInvalidQuery
	}

	queryContext, cancel := context.WithTimeoutCause(requestContext, e.timeout, execution.ErrQueryTimeout)
	defer cancel()

	client, err := e.runtime.open(queryContext, request.Source.ID, accessModeQuery)
	if err != nil {
		return result, queryContextError(requestContext, queryContext, err)
	}
	if client == nil || client.conn == nil {
		if client != nil {
			_ = client.close()
		}
		return result, execution.ErrInternal
	}
	defer func() {
		if err := client.close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
		if returnErr != nil {
			result = execution.RelationalQueryResult{}
		}
	}()

	stmt, tail, err := client.conn.Prepare(request.Query)
	if err != nil {
		return result, queryContextError(requestContext, queryContext, err)
	}
	if isNilInterface(stmt) {
		return result, execution.ErrInvalidQuery
	}
	defer func() {
		if err := stmt.Close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
		if returnErr != nil {
			result = execution.RelationalQueryResult{}
		}
	}()

	if strings.TrimSpace(tail) != "" || stmt.BindCount() != 0 || stmt.ColumnCount() == 0 {
		return result, execution.ErrInvalidQuery
	}

	result = execution.RelationalQueryResult{
		Kind:     request.Source.Kind,
		SourceID: request.Source.ID,
	}
	result, err = e.readResult(queryContext, stmt, result)
	if err != nil {
		return execution.RelationalQueryResult{}, queryContextError(requestContext, queryContext, err)
	}
	return result, nil
}

func queryContextError(requestContext, queryContext context.Context, err error) error {
	if requestContext != nil {
		switch requestContext.Err() {
		case context.Canceled:
			return fmt.Errorf("%w: %w", execution.ErrCancelled, context.Canceled)
		case context.DeadlineExceeded:
			return fmt.Errorf("%w: %w", execution.ErrQueryTimeout, context.DeadlineExceeded)
		}
	}
	if queryContext != nil {
		if errors.Is(queryContext.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", execution.ErrQueryTimeout, err)
		}
		if errors.Is(queryContext.Err(), context.Canceled) {
			return fmt.Errorf("%w: %w", execution.ErrQueryCancelled, err)
		}
	}
	known := []error{
		execution.ErrInvalidRequest,
		execution.ErrInvalidQuery,
		execution.ErrReadOnlyViolation,
		execution.ErrQueryCancelled,
		execution.ErrResultTooLarge,
		execution.ErrCancelled,
		execution.ErrQueryTimeout,
		execution.ErrDatabasePermissionDenied,
		execution.ErrDatabaseConflict,
		execution.ErrDatabaseUnavailable,
		execution.ErrDatabaseResourceExhausted,
		execution.ErrDatabaseError,
		execution.ErrInternal,
	}
	for _, sentinel := range known {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	return err
}
