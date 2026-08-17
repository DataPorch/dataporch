package execution

import (
	"context"
	"errors"
	"strings"

	"github.com/adamraziv/dataporch/internal/connection"
)

type DatabaseError struct {
	Kind                connection.Kind `json:"kind"`
	Code                string          `json:"code,omitempty"`
	ExtendedCode        string          `json:"extended_code,omitempty"`
	Severity            string          `json:"severity,omitempty"`
	SeverityUnlocalized string          `json:"severity_unlocalized,omitempty"`
	Message             string          `json:"message,omitempty"`
	Truncated           bool            `json:"truncated,omitempty"`
	Detail              string          `json:"detail,omitempty"`
	Hint                string          `json:"hint,omitempty"`
	Position            int32           `json:"position,omitempty"`
	InternalPosition    int32           `json:"internal_position,omitempty"`
	InternalQuery       string          `json:"internal_query,omitempty"`
	Where               string          `json:"where,omitempty"`
	SchemaName          string          `json:"schema_name,omitempty"`
	TableName           string          `json:"table_name,omitempty"`
	ColumnName          string          `json:"column_name,omitempty"`
	DataTypeName        string          `json:"data_type_name,omitempty"`
	ConstraintName      string          `json:"constraint_name,omitempty"`
	File                string          `json:"file,omitempty"`
	Line                int32           `json:"line,omitempty"`
	Routine             string          `json:"routine,omitempty"`
}

func (e *DatabaseError) Error() string {
	if e == nil || e.Message == "" {
		return "database error"
	}

	return e.Message
}

type DatabaseFailure struct {
	category      ErrorCategory
	retryable     bool
	databaseError *DatabaseError
}

func NewDatabaseFailure(
	category ErrorCategory,
	retryable bool,
	databaseError *DatabaseError,
) *DatabaseFailure {
	return &DatabaseFailure{
		category:      category,
		retryable:     retryable,
		databaseError: databaseError,
	}
}

func (f *DatabaseFailure) Error() string {
	if f == nil || f.databaseError == nil {
		return "database failure"
	}

	return f.databaseError.Error()
}

func (f *DatabaseFailure) Unwrap() error {
	if f == nil {
		return nil
	}

	return f.databaseError
}

var (
	ErrInvalidQuery                 = errors.New("execution: invalid query")
	ErrReadOnlyViolation            = errors.New("execution: read only violation")
	ErrQueryCancelled               = errors.New("execution: query cancelled")
	ErrResultTooLarge               = errors.New("execution: result too large")
	ErrDatabaseAuthenticationFailed = errors.New("execution: database authentication failed")
	ErrDatabaseConflict             = errors.New("execution: database conflict")
	ErrDatabaseResourceExhausted    = errors.New("execution: database resource exhausted")
	ErrDatabaseError                = errors.New("execution: database error")
)

const (
	ErrorCategorySourceKindMismatch           ErrorCategory = "source_kind_mismatch"
	ErrorCategoryInvalidQuery                 ErrorCategory = "invalid_query"
	ErrorCategoryReadOnlyViolation            ErrorCategory = "read_only_violation"
	ErrorCategoryQueryCancelled               ErrorCategory = "query_cancelled"
	ErrorCategoryResultTooLarge               ErrorCategory = "result_too_large"
	ErrorCategoryDatabaseAuthenticationFailed ErrorCategory = "database_authentication_failed"
	ErrorCategoryDatabaseConflict             ErrorCategory = "database_conflict"
	ErrorCategoryDatabaseResourceExhausted    ErrorCategory = "database_resource_exhausted"
	ErrorCategoryDatabaseError                ErrorCategory = "database_error"
)

//nolint:gocyclo // Query classification preserves explicit precedence for every stable failure category.
func ClassifyRelationalQuery(ctx context.Context, err error) Failure {
	if err == nil {
		return Failure{}
	}

	if ctx != nil {
		switch ctx.Err() {
		case context.Canceled:
			return relationalQueryFailure(ErrorCategoryCancelled, nil)
		case context.DeadlineExceeded:
			return relationalQueryFailure(ErrorCategoryQueryTimeout, nil)
		}
	}

	if errors.Is(err, ErrResultTooLarge) {
		return relationalQueryFailure(ErrorCategoryResultTooLarge, nil)
	}

	var projectedFailure *DatabaseFailure
	if errors.As(err, &projectedFailure) && projectedFailure != nil {
		return databaseFailure(projectedFailure.category, projectedFailure.retryable, projectedFailure.databaseError)
	}

	var databaseError *DatabaseError
	if errors.As(err, &databaseError) && databaseError != nil {
		return classifyDatabaseError(databaseError)
	}

	switch {
	case errors.Is(err, ErrInvalidRequest), errors.Is(err, ErrInvalidLimit):
		return relationalQueryFailure(ErrorCategoryInvalidRequest, nil)
	case errors.Is(err, ErrSourceNotFound):
		return relationalQueryFailure(ErrorCategorySourceNotFound, nil)
	case errors.Is(err, ErrSourceKindMismatch):
		return relationalQueryFailure(ErrorCategorySourceKindMismatch, nil)
	case errors.Is(err, ErrDataPorchAccessDenied):
		return relationalQueryFailure(ErrorCategoryDataPorchAccessDenied, nil)
	case errors.Is(err, ErrInvalidQuery):
		return relationalQueryFailure(ErrorCategoryInvalidQuery, nil)
	case errors.Is(err, ErrReadOnlyViolation):
		return relationalQueryFailure(ErrorCategoryReadOnlyViolation, nil)
	case errors.Is(err, ErrQueryCancelled):
		return relationalQueryFailure(ErrorCategoryQueryCancelled, nil)
	case errors.Is(err, ErrQueryTimeout), errors.Is(err, context.DeadlineExceeded):
		return relationalQueryFailure(ErrorCategoryQueryTimeout, nil)
	case errors.Is(err, ErrCancelled), errors.Is(err, context.Canceled):
		return relationalQueryFailure(ErrorCategoryCancelled, nil)
	case errors.Is(err, ErrDatabasePermissionDenied):
		return relationalQueryFailure(ErrorCategoryDatabasePermissionDenied, nil)
	case errors.Is(err, ErrDatabaseAuthenticationFailed):
		return relationalQueryFailure(ErrorCategoryDatabaseAuthenticationFailed, nil)
	case errors.Is(err, ErrDatabaseConflict):
		return relationalQueryFailure(ErrorCategoryDatabaseConflict, nil)
	case errors.Is(err, ErrDatabaseUnavailable):
		return relationalQueryFailure(ErrorCategoryDatabaseUnavailable, nil)
	case errors.Is(err, ErrDatabaseResourceExhausted):
		return relationalQueryFailure(ErrorCategoryDatabaseResourceExhausted, nil)
	case errors.Is(err, ErrDatabaseError):
		return relationalQueryFailure(ErrorCategoryDatabaseError, nil)
	default:
		return relationalQueryFailure(ErrorCategoryInternal, nil)
	}
}

//nolint:gocyclo // SQLSTATE mapping is intentionally explicit and ordered by specificity.
func classifyDatabaseError(databaseError *DatabaseError) Failure {
	if databaseError == nil || databaseError.Kind != connection.Kind("postgres") {
		return databaseFailure(ErrorCategoryDatabaseError, false, databaseError)
	}

	code := databaseError.Code

	switch {
	case code == "42501":
		return databaseFailure(ErrorCategoryDatabasePermissionDenied, false, databaseError)
	case code == "25006" || code == "2F002" || code == "38002":
		return databaseFailure(ErrorCategoryReadOnlyViolation, false, databaseError)
	case code == "57014":
		return databaseFailure(ErrorCategoryQueryCancelled, false, databaseError)
	case code == "25P03" || code == "25P04":
		return databaseFailure(ErrorCategoryQueryTimeout, false, databaseError)
	case strings.HasPrefix(code, "28"):
		return databaseFailure(ErrorCategoryDatabaseAuthenticationFailed, false, databaseError)
	case code == "40001" || code == "40P01" || code == "55P03":
		return databaseFailure(ErrorCategoryDatabaseConflict, true, databaseError)
	case strings.HasPrefix(code, "08"):
		return databaseFailure(ErrorCategoryDatabaseUnavailable, true, databaseError)
	case code == "57P01" || code == "57P02" || code == "57P03" || code == "57P04":
		return databaseFailure(ErrorCategoryDatabaseUnavailable, true, databaseError)
	case strings.HasPrefix(code, "58"):
		return databaseFailure(ErrorCategoryDatabaseUnavailable, true, databaseError)
	case code == "53300":
		return databaseFailure(ErrorCategoryDatabaseUnavailable, true, databaseError)
	case strings.HasPrefix(code, "53") || strings.HasPrefix(code, "54"):
		return databaseFailure(ErrorCategoryDatabaseResourceExhausted, false, databaseError)
	case strings.HasPrefix(code, "21") ||
		strings.HasPrefix(code, "22") ||
		strings.HasPrefix(code, "42") ||
		strings.HasPrefix(code, "0A") ||
		strings.HasPrefix(code, "3D") ||
		strings.HasPrefix(code, "3F"):
		return databaseFailure(ErrorCategoryInvalidQuery, false, databaseError)
	default:
		return databaseFailure(ErrorCategoryDatabaseError, false, databaseError)
	}
}

func databaseFailure(category ErrorCategory, retryable bool, databaseError *DatabaseError) Failure {
	message := "The database rejected the query."
	if databaseError != nil && databaseError.Message != "" {
		message = databaseError.Message
	}

	return Failure{
		Category:      category,
		Message:       message,
		Retryable:     retryable,
		DatabaseError: databaseError,
	}
}

func relationalQueryFailure(category ErrorCategory, databaseError *DatabaseError) Failure {
	messages := map[ErrorCategory]string{
		ErrorCategoryInvalidRequest:               "The query request is invalid.",
		ErrorCategorySourceNotFound:               "The data source was not found.",
		ErrorCategorySourceKindMismatch:           "The requested database kind does not match the data source.",
		ErrorCategoryDataPorchAccessDenied:        "DataPorch query access was denied.",
		ErrorCategoryInvalidQuery:                 "The query is invalid or does not return columns.",
		ErrorCategoryDatabasePermissionDenied:     "The database denied query access.",
		ErrorCategoryReadOnlyViolation:            "The query violates the read-only transaction.",
		ErrorCategoryQueryCancelled:               "The database cancelled the query.",
		ErrorCategoryQueryTimeout:                 "The query timed out.",
		ErrorCategoryResultTooLarge:               "The encoded query result is too large.",
		ErrorCategoryDatabaseAuthenticationFailed: "Database authentication failed.",
		ErrorCategoryDatabaseConflict:             "The query conflicted with concurrent database work.",
		ErrorCategoryDatabaseUnavailable:          "The database is unavailable.",
		ErrorCategoryDatabaseResourceExhausted:    "The database exhausted a resource while running the query.",
		ErrorCategoryDatabaseError:                "The database rejected the query.",
		ErrorCategoryCancelled:                    "The request was cancelled.",
		ErrorCategoryInternal:                     "The query operation failed safely.",
	}

	return Failure{
		Category:      category,
		Message:       messages[category],
		Retryable:     category == ErrorCategoryDatabaseUnavailable || category == ErrorCategoryDatabaseConflict,
		DatabaseError: databaseError,
	}
}
