package sqlite

import (
	"context"
	"errors"
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

func validateQueryRequest(
	requestContext context.Context,
	request execution.RelationalQueryExecutionRequest,
) error {
	if requestContext == nil {
		return execution.ErrCancelled
	}

	if err := requestContext.Err(); err != nil {
		return projectSQLiteError(requestContext, nil, err, sqliteErrorPhasePrepare)
	}

	if request.Source.ID == "" || request.Source.Kind != Kind {
		return execution.ErrInvalidRequest
	}

	if strings.TrimSpace(request.Query) == "" {
		return execution.ErrInvalidQuery
	}

	return nil
}

//nolint:gocyclo // Query execution keeps request validation, resource ownership, and error projection in one lifecycle.
func (e *QueryExecutor) Query(
	requestContext context.Context,
	request execution.RelationalQueryExecutionRequest,
) (result execution.RelationalQueryResult, returnErr error) {
	if err := validateQueryRequest(requestContext, request); err != nil {
		return result, err
	}

	queryContext, cancel := context.WithTimeoutCause(requestContext, e.timeout, execution.ErrQueryTimeout)
	defer cancel()

	client, err := e.runtime.open(queryContext, request.Source.ID, accessModeQuery)
	if err != nil {
		return result, projectSQLiteError(requestContext, queryContext, err, sqliteErrorPhaseOpen)
	}

	if client == nil || client.conn == nil {
		if client != nil {
			_ = client.close()
		}

		return result, execution.ErrInternal
	}
	defer func() {
		if err := client.close(); err != nil {
			returnErr = errors.Join(returnErr, projectSQLiteError(requestContext, queryContext, err, sqliteErrorPhaseClose))
		}

		if returnErr != nil {
			result = execution.RelationalQueryResult{}
		}
	}()

	stmt, tail, err := client.conn.Prepare(request.Query)
	if err != nil {
		return result, projectSQLiteError(requestContext, queryContext, err, sqliteErrorPhasePrepare)
	}

	if isNilInterface(stmt) {
		return result, execution.ErrInvalidQuery
	}
	defer func() {
		if err := stmt.Close(); err != nil {
			returnErr = errors.Join(returnErr, projectSQLiteError(requestContext, queryContext, err, sqliteErrorPhaseClose))
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
		return execution.RelationalQueryResult{}, projectSQLiteError(requestContext, queryContext, err, sqliteErrorPhaseStep)
	}

	return result, nil
}
