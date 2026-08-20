package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
)

var (
	errQueryOpenerRequired            = errors.New("mysql: query opener is required")
	errRelationalQueryTimeoutRequired = errors.New("mysql: query timeout is required")
	errQueryByteLimitRequired         = errors.New("mysql: query byte limit is required")
	errQueryRowLimitRequired          = errors.New("mysql: query row limit is required")
)

type queryClientOpener interface {
	OpenQuery(context.Context, connection.ID) (*Client, error)
}

type QueryOptions struct {
	Timeout           time.Duration
	ResponseByteLimit int
	TruncationEnabled bool
	RowLimit          int
}

type QueryExecutor struct {
	opener  queryClientOpener
	timeout time.Duration
	results queryResultReader
}

var _ execution.RelationalQueryExecutor = (*QueryExecutor)(nil)

func NewQueryExecutor(opener queryClientOpener, options QueryOptions) (*QueryExecutor, error) {
	if isNilInterface(opener) {
		return nil, errQueryOpenerRequired
	}

	if options.Timeout <= 0 {
		return nil, errRelationalQueryTimeoutRequired
	}

	if options.ResponseByteLimit <= 0 {
		return nil, errQueryByteLimitRequired
	}

	if options.TruncationEnabled && options.RowLimit <= 0 {
		return nil, errQueryRowLimitRequired
	}

	return &QueryExecutor{
		opener:  opener,
		timeout: options.Timeout,
		results: queryResultReader{
			responseByteLimit: options.ResponseByteLimit,
			truncationEnabled: options.TruncationEnabled,
			rowLimit:          options.RowLimit,
		},
	}, nil
}

func (*QueryExecutor) Kind() connection.Kind { return Kind }

//nolint:gocyclo // Query orchestration intentionally enumerates each bounded resource and cleanup boundary.
func (e *QueryExecutor) Query(
	requestContext context.Context,
	request execution.RelationalQueryExecutionRequest,
) (result execution.RelationalQueryResult, returnErr error) {
	if requestContext == nil {
		return result, execution.ErrCancelled
	}

	if err := requestContext.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return result, fmt.Errorf("%w: %w", execution.ErrQueryTimeout, err)
		}

		return result, fmt.Errorf("%w: %w", execution.ErrCancelled, err)
	}

	if request.Source.ID == "" || request.Source.Kind != Kind || strings.TrimSpace(request.Query) == "" {
		return result, execution.ErrInvalidRequest
	}

	queryContext, cancel := context.WithTimeout(requestContext, e.timeout)
	defer cancel()

	client, err := e.opener.OpenQuery(queryContext, request.Source.ID)
	if err != nil {
		return result, e.executionError(requestContext, queryContext, err)
	}

	if client == nil || client.pool == nil {
		return result, execution.ErrInternal
	}

	pool, ok := client.pool.(queryPool)
	if !ok {
		return result, execution.ErrInternal
	}

	acquired, err := pool.Acquire(queryContext)
	if err != nil {
		return result, e.executionError(requestContext, queryContext, err)
	}

	if acquired == nil {
		return result, execution.ErrInternal
	}

	var transaction queryTransaction
	defer func() {
		cleanupErr := e.cleanup(acquired, transaction)
		if cleanupErr != nil {
			result = execution.RelationalQueryResult{}
			returnErr = errors.Join(
				returnErr,
				e.executionError(requestContext, queryContext, cleanupErr),
			)
		}
	}()

	transaction, err = acquired.BeginTx(queryContext, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, e.executionError(requestContext, queryContext, err)
	}

	if transaction == nil {
		return result, execution.ErrInternal
	}

	rows, err := transaction.QueryContext(queryContext, request.Query)
	if err != nil {
		if rows != nil {
			closeErr := rows.Close()
			err = errors.Join(err, closeErr, rows.Err())
		}

		return result, e.executionError(requestContext, queryContext, err)
	}

	result, err = e.results.readResult(queryContext, rows, request)
	if err != nil {
		return execution.RelationalQueryResult{}, e.executionError(
			requestContext,
			queryContext,
			err,
		)
	}

	return result, nil
}

func (e *QueryExecutor) cleanup(
	connection queryConnection,
	transaction queryTransaction,
) error {
	if connection == nil {
		return nil
	}

	var rollbackErr error
	if transaction != nil {
		rollbackErr = transaction.Rollback()
	}

	return errors.Join(rollbackErr, connection.Destroy())
}

func (*QueryExecutor) executionError(
	requestContext context.Context,
	queryContext context.Context,
	err error,
) error {
	if requestContext != nil {
		if requestErr := requestContext.Err(); requestErr != nil {
			if errors.Is(requestErr, context.DeadlineExceeded) {
				return fmt.Errorf("%w: %w", execution.ErrQueryTimeout, requestErr)
			}

			return fmt.Errorf("%w: %w", execution.ErrCancelled, requestErr)
		}
	}

	if queryContext != nil {
		if queryErr := queryContext.Err(); queryErr != nil {
			return fmt.Errorf("%w: %w", execution.ErrQueryTimeout, queryErr)
		}
	}

	switch {
	case errors.Is(err, execution.ErrInvalidRequest),
		errors.Is(err, execution.ErrInvalidQuery),
		errors.Is(err, execution.ErrResultTooLarge),
		errors.Is(err, execution.ErrCancelled),
		errors.Is(err, execution.ErrQueryTimeout),
		errors.Is(err, execution.ErrDatabasePermissionDenied),
		errors.Is(err, execution.ErrDatabaseUnavailable),
		errors.Is(err, execution.ErrDatabaseConflict),
		errors.Is(err, execution.ErrInternal):
		return err
	default:
		return projectRelationalQueryError(err)
	}
}
