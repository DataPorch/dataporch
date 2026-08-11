package execution

import (
	"context"
	"errors"
)

type ErrorCategory string

const (
	ErrorCategoryInvalidRequest              ErrorCategory = "invalid_request"
	ErrorCategoryInvalidCursor               ErrorCategory = "invalid_cursor"
	ErrorCategorySourceNotFound              ErrorCategory = "source_not_found"
	ErrorCategoryUnsupportedSourceCapability ErrorCategory = "unsupported_source_capability"
	ErrorCategorySchemaNotFound              ErrorCategory = "schema_not_found"
	ErrorCategoryRelationNotFound            ErrorCategory = "relation_not_found"
	ErrorCategoryUnsupportedRelationKind     ErrorCategory = "unsupported_relation_kind"
	ErrorCategoryDataPorchAccessDenied       ErrorCategory = "dataporch_access_denied"
	ErrorCategoryDatabasePermissionDenied    ErrorCategory = "database_permission_denied"
	ErrorCategoryDatabaseUnavailable         ErrorCategory = "database_unavailable"
	ErrorCategoryQueryTimeout                ErrorCategory = "query_timeout"
	ErrorCategoryCancelled                   ErrorCategory = "cancelled"
	ErrorCategoryInternal                    ErrorCategory = "internal"
)

var (
	ErrInvalidLimit                = errors.New("execution: invalid resource limit")
	ErrInvalidRequest              = errors.New("execution: invalid request")
	ErrInvalidCursor               = errors.New("execution: invalid cursor")
	ErrSourceNotFound              = errors.New("execution: source not found")
	ErrUnsupportedSourceCapability = errors.New("execution: unsupported source capability")
	ErrSchemaNotFound              = errors.New("execution: schema not found")
	ErrRelationNotFound            = errors.New("execution: relation not found")
	ErrUnsupportedRelationKind     = errors.New("execution: unsupported relation kind")
	ErrDataPorchAccessDenied       = errors.New("execution: dataporch access denied")
	ErrDatabasePermissionDenied    = errors.New("execution: database permission denied")
	ErrDatabaseUnavailable         = errors.New("execution: database unavailable")
	ErrQueryTimeout                = errors.New("execution: query timeout")
	ErrCancelled                   = errors.New("execution: cancelled")
	ErrInternal                    = errors.New("execution: internal failure")
	errContextRequired             = errors.New("execution: context is required")
)

type Failure struct {
	Category  ErrorCategory `json:"category"`
	Message   string        `json:"message"`
	Retryable bool          `json:"retryable"`
}

func Classify(err error) Failure {
	if err == nil {
		return Failure{}
	}

	category := ErrorCategoryInternal
	switch {
	case errors.Is(err, ErrInvalidCursor):
		category = ErrorCategoryInvalidCursor
	case errors.Is(err, ErrInvalidRequest), errors.Is(err, ErrInvalidLimit):
		category = ErrorCategoryInvalidRequest
	case errors.Is(err, ErrSourceNotFound):
		category = ErrorCategorySourceNotFound
	case errors.Is(err, ErrUnsupportedSourceCapability):
		category = ErrorCategoryUnsupportedSourceCapability
	case errors.Is(err, ErrSchemaNotFound):
		category = ErrorCategorySchemaNotFound
	case errors.Is(err, ErrRelationNotFound):
		category = ErrorCategoryRelationNotFound
	case errors.Is(err, ErrUnsupportedRelationKind):
		category = ErrorCategoryUnsupportedRelationKind
	case errors.Is(err, ErrDataPorchAccessDenied):
		category = ErrorCategoryDataPorchAccessDenied
	case errors.Is(err, ErrDatabasePermissionDenied):
		category = ErrorCategoryDatabasePermissionDenied
	case errors.Is(err, ErrQueryTimeout), errors.Is(err, context.DeadlineExceeded):
		category = ErrorCategoryQueryTimeout
	case errors.Is(err, ErrCancelled), errors.Is(err, context.Canceled):
		category = ErrorCategoryCancelled
	case errors.Is(err, ErrDatabaseUnavailable):
		category = ErrorCategoryDatabaseUnavailable
	case errors.Is(err, ErrInternal):
		category = ErrorCategoryInternal
	}

	return failureForCategory(category)
}

func failureForCategory(category ErrorCategory) Failure {
	messages := map[ErrorCategory]string{
		ErrorCategoryInvalidRequest:              "The request is invalid.",
		ErrorCategoryInvalidCursor:               "The cursor is invalid or no longer matches the request.",
		ErrorCategorySourceNotFound:              "The data source was not found.",
		ErrorCategoryUnsupportedSourceCapability: "The data source does not support relational discovery.",
		ErrorCategorySchemaNotFound:              "The schema was not found.",
		ErrorCategoryRelationNotFound:            "The relation was not found.",
		ErrorCategoryUnsupportedRelationKind:     "The relation kind is not supported.",
		ErrorCategoryDataPorchAccessDenied:       "DataPorch access was denied.",
		ErrorCategoryDatabasePermissionDenied:    "The database denied access to the requested metadata.",
		ErrorCategoryDatabaseUnavailable:         "The database is unavailable.",
		ErrorCategoryQueryTimeout:                "The metadata query timed out.",
		ErrorCategoryCancelled:                   "The request was cancelled.",
		ErrorCategoryInternal:                    "The operation failed safely.",
	}

	return Failure{
		Category:  category,
		Message:   messages[category],
		Retryable: category == ErrorCategoryDatabaseUnavailable || category == ErrorCategoryQueryTimeout,
	}
}
