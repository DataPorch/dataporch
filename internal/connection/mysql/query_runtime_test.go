package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"
)

type discardProbeConnector struct {
	conn *discardProbeConn
}

func (c *discardProbeConnector) Connect(context.Context) (driver.Conn, error) {
	return c.conn, nil
}

func (*discardProbeConnector) Driver() driver.Driver { return discardProbeDriver{} }

type discardProbeDriver struct{}

func (discardProbeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unexpected Driver.Open")
}

type discardProbeConn struct {
	closed       atomic.Bool
	readOnlySeen atomic.Bool
}

func (*discardProbeConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }

func (c *discardProbeConn) Close() error {
	c.closed.Store(true)
	return nil
}

func (*discardProbeConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

func (c *discardProbeConn) BeginTx(
	_ context.Context,
	options driver.TxOptions,
) (driver.Tx, error) {
	c.readOnlySeen.Store(options.ReadOnly)
	return discardProbeTx{}, nil
}

type discardProbeTx struct{}

func (discardProbeTx) Commit() error   { return nil }
func (discardProbeTx) Rollback() error { return nil }

func TestMySQLRuntimePoolAcquireAndDestroy(t *testing.T) {
	t.Parallel()

	probe := &discardProbeConn{}
	db := sql.OpenDB(&discardProbeConnector{conn: probe})

	t.Cleanup(func() { _ = db.Close() })

	pool := &mysqlRuntimePool{db: db}

	acquired, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	transaction, err := acquired.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}

	if !probe.readOnlySeen.Load() {
		t.Fatal("BeginTx() did not preserve ReadOnly=true")
	}

	if err := transaction.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if err := acquired.Destroy(); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}

	if !probe.closed.Load() {
		t.Fatal("Destroy() did not discard the physical driver connection")
	}
}

func TestMySQLQueryConnectionDestroyHandlesNilAndClosed(t *testing.T) {
	t.Parallel()

	var nilConnection *mysqlQueryConnection
	if err := nilConnection.Destroy(); err != nil {
		t.Fatalf("nil Destroy() error = %v", err)
	}

	probe := &discardProbeConn{}
	db := sql.OpenDB(&discardProbeConnector{conn: probe})

	t.Cleanup(func() { _ = db.Close() })

	sqlConnection, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("db.Conn() error = %v", err)
	}

	if err := sqlConnection.Close(); err != nil {
		t.Fatalf("sql.Conn.Close() error = %v", err)
	}

	wrapper := &mysqlQueryConnection{connection: sqlConnection}
	if err := wrapper.Destroy(); err != nil {
		t.Fatalf("closed Destroy() error = %v", err)
	}
}
