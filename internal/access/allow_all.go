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

const ActionListResources Action = "list_resources"

type AllowAll struct{}

func NewAllowAll() *AllowAll {
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
