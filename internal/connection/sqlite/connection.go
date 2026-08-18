package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	sqlite3 "github.com/ncruces/go-sqlite3"
)

const openFlags = sqlite3.OPEN_READONLY | sqlite3.OPEN_URI | sqlite3.OPEN_NOFOLLOW

var errSQLiteFileUnavailable = errors.New("sqlite: database file unavailable")

func openPhysicalConnection(
	ctx context.Context,
	path string,
	mode accessMode,
) (ret rawConnection, retErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", errSQLiteFileUnavailable)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", errSQLiteFileUnavailable, err)
	}

	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, errSQLiteFileUnavailable
	}

	conn, err := sqlite3.OpenFlags(path, openFlags)
	if err != nil {
		return nil, fmt.Errorf("sqlite: opening database: %w", err)
	}

	physical := &physicalConnection{conn: conn}

	defer func() {
		if ret == nil {
			retErr = errors.Join(retErr, physical.Close())
		}
	}()

	physical.SetInterrupt(ctx)

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", errSQLiteFileUnavailable, err)
	}

	_, err = configureConnectionAndClose(physical, mode)
	if err != nil {
		physical = nil
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", errSQLiteFileUnavailable, err)
	}

	return physical, nil
}

func configureConnectionAndClose(conn rawConnection, mode accessMode) (rawConnection, error) {
	if err := configureConnection(conn, mode); err != nil {
		return nil, errors.Join(err, conn.Close())
	}

	return conn, nil
}

func validateSQLiteConnection(conn rawConnection) error {
	stmt, tail, err := conn.Prepare("SELECT 1 FROM main.sqlite_schema LIMIT 1")
	if err != nil {
		return fmt.Errorf("sqlite: validating database: %w", err)
	}

	if stmt == nil || strings.TrimSpace(tail) != "" {
		if stmt != nil {
			_ = stmt.Close()
		}

		return fmt.Errorf("%w: invalid schema statement", errSQLiteFileUnavailable)
	}

	stmt.Step()
	stepErr := stmt.Err()

	closeErr := stmt.Close()
	if stepErr != nil {
		return errors.Join(fmt.Errorf("sqlite: validating database: %w", stepErr), closeErr)
	}

	if closeErr != nil {
		return fmt.Errorf("sqlite: closing validation statement: %w", closeErr)
	}

	return nil
}

type physicalConnection struct {
	conn *sqlite3.Conn
}

func (c *physicalConnection) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}

	return c.conn.Close()
}

func (c *physicalConnection) Config(op sqlite3.DBConfig, arg ...bool) (bool, error) {
	return c.conn.Config(op, arg...)
}

func (c *physicalConnection) Exec(sql string) error {
	return c.conn.Exec(sql)
}

func (c *physicalConnection) Prepare(sql string) (statement, string, error) {
	return c.conn.Prepare(sql)
}

func (c *physicalConnection) SetAuthorizer(callback func(sqlite3.AuthorizerActionCode, string, string, string, string) sqlite3.AuthorizerReturnCode) error {
	return c.conn.SetAuthorizer(callback)
}

func (c *physicalConnection) SetInterrupt(ctx context.Context) context.Context {
	return c.conn.SetInterrupt(ctx)
}

var _ rawConnection = (*physicalConnection)(nil)
