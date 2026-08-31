package access

import (
	"context"
	"errors"

	"github.com/DataPorch/dataporch/internal/connection"
)

var (
	errContextRequired = errors.New("access: context is required")
	errActionRequired  = errors.New("access: action is required")
)

type Action string

const (
	ActionListDataSources         Action = "list_data_sources"
	ActionListRelationalSchemas   Action = "list_relational_database_schemas"
	ActionListRelationalTables    Action = "list_relational_database_tables"
	ActionListRelationalColumns   Action = "list_relational_database_columns"
	ActionQueryRelationalDatabase Action = "query_relational_database"
)

type Request struct {
	Action   Action
	Kind     connection.Kind
	SourceID connection.ID
}

type AllowAll struct{}

func New() *AllowAll {
	return &AllowAll{}
}

func (a *AllowAll) Authorize(ctx context.Context, request Request) error {
	if ctx == nil {
		return errContextRequired
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if request.Action == "" {
		return errActionRequired
	}

	return nil
}
