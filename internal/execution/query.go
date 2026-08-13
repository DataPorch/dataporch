package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/adamraziv/dataporch/internal/access"
	"github.com/adamraziv/dataporch/internal/connection"
)

var ErrSourceKindMismatch = errors.New("execution: source kind mismatch")

type RelationalQueryRequest struct {
	Kind     connection.Kind
	SourceID connection.ID
	Query    string
}

type RelationalQueryExecutionRequest struct {
	Source connection.Definition
	Query  string
}

type RelationalQueryColumn struct {
	Name         string `json:"name"`
	DatabaseType string `json:"database_type"`
}

type RelationalQueryResult struct {
	Kind      connection.Kind         `json:"kind"`
	SourceID  connection.ID           `json:"source_id"`
	Columns   []RelationalQueryColumn `json:"columns"`
	Rows      [][]*string             `json:"rows"`
	RowCount  int                     `json:"row_count"`
	Truncated bool                    `json:"truncated"`
}

type RelationalQueryExecutor interface {
	Kind() connection.Kind
	Query(context.Context, RelationalQueryExecutionRequest) (RelationalQueryResult, error)
}

func (s *Service) QueryRelationalDatabase(
	ctx context.Context,
	request RelationalQueryRequest,
) (RelationalQueryResult, error) {
	if err := validateContext(ctx); err != nil {
		return RelationalQueryResult{}, err
	}

	if request.Kind == "" || request.SourceID == "" || strings.TrimSpace(request.Query) == "" {
		return RelationalQueryResult{}, ErrInvalidRequest
	}

	executor, supported := s.relationalQueries[request.Kind]
	if !supported {
		return RelationalQueryResult{}, fmt.Errorf(
			"%w: unsupported relational query kind %q",
			ErrInvalidRequest,
			request.Kind,
		)
	}

	if err := validateSourceID(request.SourceID); err != nil {
		return RelationalQueryResult{}, err
	}

	if err := s.authorizer.Authorize(ctx, access.Request{
		Action:   access.ActionQueryRelationalDatabase,
		Kind:     request.Kind,
		SourceID: request.SourceID,
	}); err != nil {
		return RelationalQueryResult{}, fmt.Errorf("%w: %w", ErrDataPorchAccessDenied, err)
	}

	definition, err := s.sources.Lookup(request.SourceID)
	if err != nil {
		if errors.Is(err, connection.ErrDatabaseNotFound) {
			return RelationalQueryResult{}, fmt.Errorf("%w: %s", ErrSourceNotFound, request.SourceID)
		}

		return RelationalQueryResult{}, fmt.Errorf("%w: source lookup failed", ErrInternal)
	}

	if definition.Kind != request.Kind {
		return RelationalQueryResult{}, fmt.Errorf(
			"%w: requested %s, stored %s",
			ErrSourceKindMismatch,
			request.Kind,
			definition.Kind,
		)
	}

	result, err := executor.Query(ctx, RelationalQueryExecutionRequest{
		Source: definition,
		Query:  request.Query,
	})
	if err != nil {
		return RelationalQueryResult{}, err
	}

	result.Kind = definition.Kind
	result.SourceID = definition.ID

	return result, nil
}
