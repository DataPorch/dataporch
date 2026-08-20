package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
)

type queryPool interface {
	Acquire(context.Context) (queryConnection, error)
}

type queryConnection interface {
	BeginTx(context.Context, *sql.TxOptions) (queryTransaction, error)
	Destroy() error
}

type queryTransaction interface {
	QueryContext(context.Context, string, ...any) (queryRows, error)
	Rollback() error
}

type queryRows interface {
	Close() error
	Columns() ([]string, error)
	ColumnTypes() ([]*sql.ColumnType, error)
	Err() error
	Next() bool
	Scan(...any) error
}

type mysqlQueryConnection struct {
	connection *sql.Conn
}

type mysqlQueryTransaction struct {
	transaction *sql.Tx
}

var _ queryPool = (*mysqlRuntimePool)(nil)

func (p *mysqlRuntimePool) Acquire(ctx context.Context) (queryConnection, error) {
	if p == nil || p.db == nil {
		return nil, errInvalidRuntimeDefinition
	}

	connection, err := p.db.Conn(ctx)
	if err != nil {
		return nil, err
	}

	return &mysqlQueryConnection{connection: connection}, nil
}

func (c *mysqlQueryConnection) BeginTx(
	ctx context.Context,
	options *sql.TxOptions,
) (queryTransaction, error) {
	if c == nil || c.connection == nil {
		return nil, sql.ErrConnDone
	}

	transaction, err := c.connection.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}

	return &mysqlQueryTransaction{transaction: transaction}, nil
}

func (t *mysqlQueryTransaction) QueryContext(
	ctx context.Context,
	query string,
	args ...any,
) (queryRows, error) {
	if t == nil || t.transaction == nil {
		return nil, sql.ErrTxDone
	}

	rows, err := t.transaction.QueryContext(ctx, query, args...) //nolint:rowserrcheck // queryResultReader consumes Rows.Err.
	if rows == nil {
		return nil, err
	}

	return rows, err
}

func (t *mysqlQueryTransaction) Rollback() error {
	if t == nil || t.transaction == nil {
		return nil
	}

	return t.transaction.Rollback()
}

func (c *mysqlQueryConnection) Destroy() error {
	if c == nil || c.connection == nil {
		return nil
	}

	err := c.connection.Raw(func(any) error {
		return driver.ErrBadConn
	})
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return nil
	}

	return err
}
