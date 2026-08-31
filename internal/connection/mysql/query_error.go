package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/DataPorch/dataporch/internal/execution"
	gomysql "github.com/go-sql-driver/mysql"
)

type mysqlRelationalQueryError struct {
	classification error
	cause          error
}

func (e *mysqlRelationalQueryError) Error() string {
	if e == nil || e.classification == nil {
		return execution.ErrInternal.Error()
	}

	return e.classification.Error()
}

func (e *mysqlRelationalQueryError) Unwrap() []error {
	if e == nil {
		return nil
	}

	return []error{e.classification, e.cause}
}

func projectMySQLError(mysqlErr *gomysql.MySQLError) *execution.DatabaseError {
	if mysqlErr == nil {
		return &execution.DatabaseError{
			Kind:    Kind,
			Code:    "0",
			Message: "MySQL database error.",
		}
	}

	return &execution.DatabaseError{
		Kind:         Kind,
		Code:         strconv.FormatUint(uint64(mysqlErr.Number), 10),
		ExtendedCode: mysqlSQLState(mysqlErr.SQLState),
		Message:      mysqlDiagnostic(mysqlErr.Message),
	}
}

func mysqlSQLState(state [5]byte) string {
	if state == [5]byte{} {
		return ""
	}

	return string(state[:])
}

func mysqlErrorCategory(number uint16, sqlState string) (execution.ErrorCategory, bool) {
	sqlState = strings.ToUpper(sqlState)

	switch number {
	case 1045:
		return execution.ErrorCategoryDatabaseAuthenticationFailed, false
	case 1044, 1142, 1143, 1227:
		return execution.ErrorCategoryDatabasePermissionDenied, false
	case 1792:
		return execution.ErrorCategoryReadOnlyViolation, false
	case 1317:
		return execution.ErrorCategoryQueryCancelled, false
	case 3024:
		return execution.ErrorCategoryQueryTimeout, false
	case 1213, 1205:
		return execution.ErrorCategoryDatabaseConflict, true
	case 1040:
		return execution.ErrorCategoryDatabaseUnavailable, true
	case 1041, 1037, 1114:
		return execution.ErrorCategoryDatabaseResourceExhausted, false
	}

	switch {
	case sqlState == "25006":
		return execution.ErrorCategoryReadOnlyViolation, false
	case strings.HasPrefix(sqlState, "08"):
		return execution.ErrorCategoryDatabaseUnavailable, true
	case strings.HasPrefix(sqlState, "21"),
		strings.HasPrefix(sqlState, "22"),
		strings.HasPrefix(sqlState, "42"),
		strings.HasPrefix(sqlState, "0A"),
		strings.HasPrefix(sqlState, "3D"),
		strings.HasPrefix(sqlState, "3F"):
		return execution.ErrorCategoryInvalidQuery, false
	default:
		return execution.ErrorCategoryDatabaseError, false
	}
}

func projectRelationalQueryError(err error) error {
	return projectMySQLQueryError(context.Background(), context.Background(), err)
}

func projectMySQLQueryError(
	requestContext context.Context,
	queryContext context.Context,
	err error,
) error {
	if err == nil {
		return nil
	}

	if requestContext != nil {
		switch requestContext.Err() {
		case context.Canceled:
			return wrapMySQLClassification(execution.ErrCancelled, err)
		case context.DeadlineExceeded:
			return wrapMySQLClassification(execution.ErrQueryTimeout, err)
		}
	}

	if queryContext != nil {
		if cause := context.Cause(queryContext); cause != nil {
			switch {
			case errors.Is(cause, execution.ErrQueryTimeout), errors.Is(cause, context.DeadlineExceeded):
				return wrapMySQLClassification(execution.ErrQueryTimeout, err)
			case errors.Is(cause, execution.ErrQueryCancelled), errors.Is(cause, context.Canceled):
				return wrapMySQLClassification(execution.ErrQueryCancelled, err)
			}
		}

		switch queryContext.Err() {
		case context.DeadlineExceeded:
			return wrapMySQLClassification(execution.ErrQueryTimeout, err)
		case context.Canceled:
			return wrapMySQLClassification(execution.ErrQueryCancelled, err)
		}
	}

	return projectMySQLQueryErrorWithoutContext(err)
}

func projectMySQLQueryErrorWithoutContext(err error) error {
	if err == nil {
		return nil
	}

	if isMySQLProjectedError(err) || isMySQLKnownError(err) {
		return err
	}

	var mysqlErr *gomysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr != nil {
		category, retryable := mysqlErrorCategory(mysqlErr.Number, mysqlSQLState(mysqlErr.SQLState))
		failure := execution.NewDatabaseFailure(category, retryable, projectMySQLError(mysqlErr))

		return &mysqlRelationalQueryError{classification: failure, cause: err}
	}

	if isMySQLUnavailableError(err) {
		return wrapMySQLClassification(execution.ErrDatabaseUnavailable, err)
	}

	if errors.Is(err, context.Canceled) {
		return wrapMySQLClassification(execution.ErrCancelled, err)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return wrapMySQLClassification(execution.ErrQueryTimeout, err)
	}

	return wrapMySQLClassification(execution.ErrInternal, err)
}

func wrapMySQLClassification(classification, cause error) error {
	if cause == nil {
		return classification
	}

	return &mysqlRelationalQueryError{classification: classification, cause: cause}
}

func isMySQLProjectedError(err error) bool {
	var failure *execution.DatabaseFailure
	return errors.As(err, &failure) && failure != nil
}

func isMySQLKnownError(err error) bool {
	for _, sentinel := range []error{
		execution.ErrSchemaNotFound,
		execution.ErrRelationNotFound,
		execution.ErrUnsupportedRelationKind,
		execution.ErrInternal,
		execution.ErrCancelled,
		execution.ErrQueryTimeout,
		execution.ErrQueryCancelled,
		execution.ErrDatabasePermissionDenied,
		execution.ErrDatabaseUnavailable,
		execution.ErrDatabaseConflict,
		execution.ErrDatabaseResourceExhausted,
		execution.ErrInvalidQuery,
		execution.ErrReadOnlyViolation,
		execution.ErrDatabaseAuthenticationFailed,
		execution.ErrDatabaseError,
		errRuntimeContextRequired,
		errRuntimeInvalidID,
		ErrUnsupportedKind,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}

	return false
}

func isMySQLUnavailableError(err error) bool {
	if errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, ErrOpenTimeout) ||
		errors.Is(err, ErrRuntimeClosed) ||
		errors.Is(err, errOpenInvalidated) {
		return true
	}

	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}

	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}

	message := strings.ToLower(err.Error())

	return strings.Contains(message, "connection refused") || strings.Contains(message, "dial tcp")
}

func mysqlDiagnostic(message string) string {
	message = strings.ToValidUTF8(message, "\uFFFD")
	message = strings.ReplaceAll(message, "\x00", "")

	fields := strings.Fields(message)
	for index, field := range fields {
		if strings.Contains(field, "/") || strings.HasPrefix(field, "file:") {
			fields[index] = "[redacted]"
		}
	}

	message = strings.Join(fields, " ")
	if message == "" {
		return "MySQL database error."
	}

	if len(message) <= 512 {
		return message
	}

	message = message[:512]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}

	return message
}
