package access

import (
	"context"
	"errors"
)

var (
	errContextRequired = errors.New("access: context is required")
	errActionRequired  = errors.New("access: action is required")
)

type Action string

const (
	ActionListResources         Action = "list_resources"
	ActionListDataSources       Action = "list_data_sources"
	ActionListRelationalSchemas Action = "list_relational_database_schemas"
	ActionListRelationalTables  Action = "list_relational_database_tables"
	ActionListRelationalColumns Action = "list_relational_database_columns"
)

type AllowAll struct{}

func New() *AllowAll {
	return &AllowAll{}
}

func (a *AllowAll) Authorize(ctx context.Context, action Action) error {
	if ctx == nil {
		return errContextRequired
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if action == "" {
		return errActionRequired
	}

	return nil
}
