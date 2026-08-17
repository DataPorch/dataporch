package sqlite

import (
	"context"
	"errors"
	"testing"

	sqlite3 "github.com/ncruces/go-sqlite3"
)

func TestAuthorizerQueryMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action sqlite3.AuthorizerActionCode
		third  string
		fourth string
		schema string
		want   sqlite3.AuthorizerReturnCode
	}{
		{name: "select", action: sqlite3.AUTH_SELECT, want: sqlite3.AUTH_OK},
		{name: "recursive", action: sqlite3.AUTH_RECURSIVE, want: sqlite3.AUTH_OK},
		{name: "main read", action: sqlite3.AUTH_READ, third: "orders", fourth: "id", schema: "main", want: sqlite3.AUTH_OK},
		{name: "temp read", action: sqlite3.AUTH_READ, third: "orders", fourth: "id", schema: "temp", want: sqlite3.AUTH_DENY},
		{name: "pragma", action: sqlite3.AUTH_PRAGMA, third: "query_only", want: sqlite3.AUTH_DENY},
		{name: "transaction", action: sqlite3.AUTH_TRANSACTION, third: "BEGIN", want: sqlite3.AUTH_DENY},
		{name: "savepoint", action: sqlite3.AUTH_SAVEPOINT, third: "BEGIN", fourth: "savepoint", want: sqlite3.AUTH_DENY},
		{name: "attach", action: sqlite3.AUTH_ATTACH, third: "other.db", want: sqlite3.AUTH_DENY},
		{name: "detach", action: sqlite3.AUTH_DETACH, third: "other", want: sqlite3.AUTH_DENY},
		{name: "insert", action: sqlite3.AUTH_INSERT, third: "orders", schema: "main", want: sqlite3.AUTH_DENY},
		{name: "unknown action", action: sqlite3.AuthorizerActionCode(999), want: sqlite3.AUTH_DENY},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := authorize(accessModeQuery, test.action, test.third, test.fourth, test.schema, "")
			if got != test.want {
				t.Fatalf("authorize() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestAuthorizerDiscoveryPragmaPolicy(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"table_list", "table_xinfo", "index_list", "index_xinfo", "foreign_key_list", "TABLE_LIST"} {
		if got := authorize(accessModeDiscovery, sqlite3.AUTH_PRAGMA, name, "", "main", ""); got != sqlite3.AUTH_OK {
			t.Fatalf("discovery pragma %q = %v, want AUTH_OK", name, got)
		}
	}
	for _, name := range []string{"query_only", "database_list", "trusted_schema", ""} {
		if got := authorize(accessModeDiscovery, sqlite3.AUTH_PRAGMA, name, "", "main", ""); got != sqlite3.AUTH_DENY {
			t.Fatalf("discovery pragma %q = %v, want AUTH_DENY", name, got)
		}
	}
}

func TestAuthorizerQueryFunctionPolicy(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"lower", "instr", "abs", "custom_function"} {
		if got := authorize(accessModeQuery, sqlite3.AUTH_FUNCTION, "", name, "main", ""); got != sqlite3.AUTH_OK {
			t.Fatalf("query function %q = %v, want AUTH_OK", name, got)
		}
	}
	for _, name := range []string{"load_extension", "readfile", "writefile", "fsdir", "pragma_table_list", "PrAgMa_Table_XInfo"} {
		if got := authorize(accessModeQuery, sqlite3.AUTH_FUNCTION, "", name, "main", ""); got != sqlite3.AUTH_DENY {
			t.Fatalf("query function %q = %v, want AUTH_DENY", name, got)
		}
	}
}

func TestAuthorizerDiscoveryFunctionPolicy(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"lower",
		"instr",
		"like",
		"pragma_table_list",
		"pragma_table_xinfo",
		"pragma_index_list",
		"pragma_index_xinfo",
		"pragma_foreign_key_list",
		"PrAgMa_Table_List",
	} {
		if got := authorize(accessModeDiscovery, sqlite3.AUTH_FUNCTION, "", name, "main", ""); got != sqlite3.AUTH_OK {
			t.Fatalf("discovery function %q = %v, want AUTH_OK", name, got)
		}
	}
	for _, name := range []string{"", "abs", "pragma_database_list", "load_extension", "readfile", "writefile", "fsdir"} {
		if got := authorize(accessModeDiscovery, sqlite3.AUTH_FUNCTION, "", name, "main", ""); got != sqlite3.AUTH_DENY {
			t.Fatalf("discovery function %q = %v, want AUTH_DENY", name, got)
		}
	}
}

func TestConfigureConnection(t *testing.T) {
	t.Parallel()

	connection := &policyConnection{}
	if err := configureConnection(connection, accessModeQuery); err != nil {
		t.Fatalf("configureConnection() error = %v", err)
	}
	if connection.configCalls != 2 || connection.execSQL != "PRAGMA query_only=ON" || connection.authorizer == nil {
		t.Fatalf("connection setup = %#v, want defensive/trusted/query_only/authorizer", connection)
	}
}

func TestConfigureConnectionFailuresClosePartialConnections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		conn func() *policyConnection
	}{
		{name: "defensive", conn: func() *policyConnection { return &policyConnection{configErrorAt: 1} }},
		{name: "trusted schema", conn: func() *policyConnection { return &policyConnection{configErrorAt: 2} }},
		{name: "query only", conn: func() *policyConnection { return &policyConnection{execErr: errors.New("query-only")} }},
		{name: "schema validation", conn: func() *policyConnection { return &policyConnection{prepareErr: errors.New("schema")} }},
		{name: "authorizer", conn: func() *policyConnection { return &policyConnection{authorizerErr: errors.New("authorizer")} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			connection := test.conn()
			if _, err := configureConnectionAndClose(connection, accessModeQuery); err == nil {
				t.Fatal("configureConnectionAndClose() error = nil, want setup error")
			}
			if connection.closeCount != 1 {
				t.Fatalf("partial connection close count = %d, want 1", connection.closeCount)
			}
		})
	}
}

type policyConnection struct {
	configCalls   int
	configErrorAt int
	execSQL       string
	execErr       error
	prepareErr    error
	authorizer    func(sqlite3.AuthorizerActionCode, string, string, string, string) sqlite3.AuthorizerReturnCode
	authorizerErr error
	closeCount    int
}

func (c *policyConnection) Close() error {
	c.closeCount++
	return nil
}
func (c *policyConnection) Config(sqlite3.DBConfig, ...bool) (bool, error) {
	c.configCalls++
	if c.configCalls == c.configErrorAt {
		return false, errors.New("config")
	}
	return c.configCalls == 1, nil
}
func (c *policyConnection) Exec(sql string) error {
	c.execSQL = sql
	return c.execErr
}
func (c *policyConnection) Prepare(string) (statement, string, error) {
	return &runtimeStatement{}, "", c.prepareErr
}
func (c *policyConnection) SetAuthorizer(callback func(sqlite3.AuthorizerActionCode, string, string, string, string) sqlite3.AuthorizerReturnCode) error {
	c.authorizer = callback
	return c.authorizerErr
}
func (*policyConnection) SetInterrupt(ctx context.Context) context.Context { return ctx }
