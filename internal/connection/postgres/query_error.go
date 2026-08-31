package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/DataPorch/dataporch/internal/connection"
	"github.com/DataPorch/dataporch/internal/execution"
	"github.com/jackc/pgx/v5/pgconn"
)

type privateRelationalQueryError struct {
	classification error
	cause          error
}

func (e *privateRelationalQueryError) Error() string {
	return e.classification.Error()
}

func (e *privateRelationalQueryError) Unwrap() []error {
	return []error{e.classification, e.cause}
}

func projectPostgresError(pgError *pgconn.PgError) *execution.DatabaseError {
	return &execution.DatabaseError{
		Kind:                Kind,
		Code:                pgError.Code,
		Severity:            pgError.Severity,
		SeverityUnlocalized: pgError.SeverityUnlocalized,
		Message:             pgError.Message,
		Detail:              pgError.Detail,
		Hint:                pgError.Hint,
		Position:            pgError.Position,
		InternalPosition:    pgError.InternalPosition,
		InternalQuery:       pgError.InternalQuery,
		Where:               pgError.Where,
		SchemaName:          pgError.SchemaName,
		TableName:           pgError.TableName,
		ColumnName:          pgError.ColumnName,
		DataTypeName:        pgError.DataTypeName,
		ConstraintName:      pgError.ConstraintName,
		File:                pgError.File,
		Line:                pgError.Line,
		Routine:             pgError.Routine,
	}
}

func projectRelationalQueryError(err error) error {
	if err == nil {
		return nil
	}

	var databaseError *execution.DatabaseError
	if errors.As(err, &databaseError) {
		return err
	}

	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		return fmt.Errorf("postgres query failed: %w", projectPostgresError(pgError))
	}

	classification := execution.ErrInternal

	switch {
	case errors.Is(err, context.Canceled):
		classification = execution.ErrCancelled
	case errors.Is(err, context.DeadlineExceeded):
		classification = execution.ErrQueryTimeout
	case errors.Is(err, ErrOpenTimeout),
		errors.Is(err, ErrRuntimeClosed),
		errors.Is(err, errOpenInvalidated),
		errors.Is(err, connection.ErrDatabaseNotFound),
		errors.Is(err, connection.ErrDatabaseUnavailable):
		classification = execution.ErrDatabaseUnavailable
	case pgconn.SafeToRetry(err):
		classification = execution.ErrDatabaseUnavailable
	}

	return &privateRelationalQueryError{
		classification: classification,
		cause:          err,
	}
}
