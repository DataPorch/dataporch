package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DataPorch/dataporch/internal/connection"
	"github.com/DataPorch/dataporch/internal/execution"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const defaultQueryCleanupTimeout = 2 * time.Second

var (
	errQueryOpenerRequired            = errors.New("postgres: query opener is required")
	errRelationalQueryTimeoutRequired = errors.New("postgres: query timeout is required")
	errQueryByteLimitRequired         = errors.New("postgres: query byte limit is required")
	errQueryCleanupRequired           = errors.New("postgres: query cleanup timeout is required")
	errQueryRowLimitRequired          = errors.New("postgres: query row limit is required")
)

type QueryOptions struct {
	Timeout           time.Duration
	ResponseByteLimit int
	TruncationEnabled bool
	RowLimit          int
}

type queryClientOpener interface {
	OpenQuery(context.Context, connection.ID) (*Client, error)
}

type QueryExecutor struct {
	opener         queryClientOpener
	timeout        time.Duration
	byteLimit      int
	truncate       bool
	rowLimit       int
	cleanupTimeout time.Duration
}

var _ execution.RelationalQueryExecutor = (*QueryExecutor)(nil)

func NewQueryExecutor(
	opener queryClientOpener,
	options QueryOptions,
) (*QueryExecutor, error) {
	return newQueryExecutor(opener, options, defaultQueryCleanupTimeout)
}

func newQueryExecutor(
	opener queryClientOpener,
	options QueryOptions,
	cleanupTimeout time.Duration,
) (*QueryExecutor, error) {
	if isNilInterface(opener) {
		return nil, errQueryOpenerRequired
	}

	if options.Timeout <= 0 {
		return nil, errRelationalQueryTimeoutRequired
	}

	if options.ResponseByteLimit <= 0 {
		return nil, errQueryByteLimitRequired
	}

	if cleanupTimeout <= 0 {
		return nil, errQueryCleanupRequired
	}

	if options.TruncationEnabled && options.RowLimit <= 0 {
		return nil, errQueryRowLimitRequired
	}

	return &QueryExecutor{
		opener:         opener,
		timeout:        options.Timeout,
		byteLimit:      options.ResponseByteLimit,
		truncate:       options.TruncationEnabled,
		rowLimit:       options.RowLimit,
		cleanupTimeout: cleanupTimeout,
	}, nil
}

func (e *QueryExecutor) Kind() connection.Kind {
	return Kind
}

//nolint:gocyclo // Query orchestration intentionally enumerates each bounded resource and cleanup boundary.
func (e *QueryExecutor) Query(
	requestContext context.Context,
	request execution.RelationalQueryExecutionRequest,
) (result execution.RelationalQueryResult, returnErr error) {
	if requestContext == nil {
		return result, execution.ErrCancelled
	}

	if err := requestContext.Err(); err != nil {
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
		cleanupErr := e.cleanup(requestContext, acquired, transaction)
		if cleanupErr != nil {
			result = execution.RelationalQueryResult{}
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()

	transaction, err = acquired.BeginTx(queryContext, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, e.executionError(requestContext, queryContext, err)
	}

	if transaction == nil {
		return result, execution.ErrInternal
	}

	rows, err := transaction.Query(queryContext, request.Query, pgx.QueryExecModeExec)
	if err != nil {
		if rows != nil {
			rows.Close()
			err = errors.Join(err, rows.Err())
		}

		return result, e.executionError(requestContext, queryContext, err)
	}

	result, err = e.readResult(queryContext, acquired, rows, request)
	if err != nil {
		return execution.RelationalQueryResult{}, e.executionError(
			requestContext,
			queryContext,
			err,
		)
	}

	return result, nil
}

func (e *QueryExecutor) executionError(
	requestContext context.Context,
	queryContext context.Context,
	err error,
) error {
	if requestContext != nil {
		switch requestContext.Err() {
		case context.Canceled:
			return fmt.Errorf("%w: %w", execution.ErrCancelled, context.Canceled)
		case context.DeadlineExceeded:
			return fmt.Errorf("%w: %w", execution.ErrQueryTimeout, context.DeadlineExceeded)
		}
	}

	if queryContext != nil && errors.Is(queryContext.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", execution.ErrQueryTimeout, err)
	}

	var databaseError *execution.DatabaseError
	if errors.As(err, &databaseError) {
		return err
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
		execution.ErrDatabaseAuthenticationFailed,
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

	return projectRelationalQueryError(err)
}

func (e *QueryExecutor) cleanup(
	requestContext context.Context,
	connection queryConnection,
	transaction queryTransaction,
) error {
	if connection == nil {
		return nil
	}

	if transaction == nil {
		connection.Release()
		return nil
	}

	cleanupContext, cancel := context.WithTimeout(
		context.WithoutCancel(requestContext),
		e.cleanupTimeout,
	)
	defer cancel()

	cleanupErr := transaction.Rollback(cleanupContext)
	if cleanupErr == nil {
		cleanupErr = connection.DeallocateAll(cleanupContext)
	}

	if cleanupErr == nil {
		cleanupErr = connection.DiscardAll(cleanupContext)
	}

	if cleanupErr == nil {
		connection.Release()
		return nil
	}

	destroyErr := connection.Destroy(cleanupContext)
	joined := errors.Join(cleanupErr, destroyErr)

	var databaseError *execution.DatabaseError
	if errors.As(joined, &databaseError) {
		return joined
	}

	var pgError *pgconn.PgError
	if errors.As(joined, &pgError) {
		return projectRelationalQueryError(joined)
	}

	return &privateRelationalQueryError{
		classification: execution.ErrInternal,
		cause:          joined,
	}
}
