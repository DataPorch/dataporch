package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type queryPool interface {
	Acquire(context.Context) (queryConnection, error)
}

type queryConnection interface {
	BeginTx(context.Context, pgx.TxOptions) (queryTransaction, error)
	DatabaseTypeName(uint32) (string, bool)
	DeallocateAll(context.Context) error
	DiscardAll(context.Context) error
	Release()
	Destroy(context.Context) error
}

type queryTransaction interface {
	Query(context.Context, string, pgx.QueryExecMode) (queryRows, error)
	Rollback(context.Context) error
}

type queryRows interface {
	Close()
	Err() error
	FieldDescriptions() []pgconn.FieldDescription
	Next() bool
	RawValues() [][]byte
}

var _ queryPool = (*pgxRuntimePool)(nil)

type pgxQueryConnection struct {
	connection *pgxpool.Conn
}

type pgxQueryTransaction struct {
	transaction pgx.Tx
}

func (p *pgxRuntimePool) Acquire(ctx context.Context) (queryConnection, error) {
	connection, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	return &pgxQueryConnection{connection: connection}, nil
}

func (c *pgxQueryConnection) BeginTx(
	ctx context.Context,
	options pgx.TxOptions,
) (queryTransaction, error) {
	transaction, err := c.connection.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}

	return &pgxQueryTransaction{transaction: transaction}, nil
}

func (c *pgxQueryConnection) DatabaseTypeName(oid uint32) (string, bool) {
	databaseType, ok := c.connection.Conn().TypeMap().TypeForOID(oid)
	if !ok || databaseType == nil {
		return "", false
	}

	return databaseType.Name, true
}

func (c *pgxQueryConnection) DeallocateAll(ctx context.Context) error {
	return c.connection.Conn().DeallocateAll(ctx)
}

func (c *pgxQueryConnection) DiscardAll(ctx context.Context) error {
	_, err := c.connection.Exec(ctx, "DISCARD ALL", pgx.QueryExecModeExec)
	return err
}

func (c *pgxQueryConnection) Release() {
	c.connection.Release()
}

func (c *pgxQueryConnection) Destroy(ctx context.Context) error {
	return c.connection.Hijack().Close(ctx)
}

func (t *pgxQueryTransaction) Query(
	ctx context.Context,
	query string,
	mode pgx.QueryExecMode,
) (queryRows, error) {
	return t.transaction.Query(ctx, query, mode)
}

func (t *pgxQueryTransaction) Rollback(ctx context.Context) error {
	return t.transaction.Rollback(ctx)
}
