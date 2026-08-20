package sqlite

import (
	"fmt"
	"strings"

	sqlite3 "github.com/ncruces/go-sqlite3"
)

func authorize(
	mode accessMode,
	action sqlite3.AuthorizerActionCode,
	name3rd string,
	name4th string,
	schema string,
	inner string,
) sqlite3.AuthorizerReturnCode {
	_, _, _, _ = name3rd, name4th, schema, inner

	//nolint:exhaustive // default denial intentionally blocks unknown SQLite actions.
	switch action {
	case sqlite3.AUTH_SELECT, sqlite3.AUTH_RECURSIVE:
		return sqlite3.AUTH_OK
	case sqlite3.AUTH_READ:
		if schema == "" || schema == sqliteMainSchema {
			return sqlite3.AUTH_OK
		}
	case sqlite3.AUTH_FUNCTION:
		if functionAllowed(mode, name4th) {
			return sqlite3.AUTH_OK
		}
	case sqlite3.AUTH_PRAGMA:
		if mode == accessModeDiscovery && pragmaAllowed(name3rd) {
			return sqlite3.AUTH_OK
		}
	}

	return sqlite3.AUTH_DENY
}

func functionAllowed(mode accessMode, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || isDeniedFunction(name) {
		return false
	}

	if mode == accessModeQuery {
		return !strings.HasPrefix(name, "pragma_")
	}

	switch name {
	case "lower", "instr", "like", "pragma_table_list", "pragma_table_xinfo", "pragma_index_list", "pragma_index_xinfo", "pragma_foreign_key_list":
		return true
	default:
		return false
	}
}

func isDeniedFunction(name string) bool {
	switch name {
	case "load_extension", "readfile", "writefile", "fsdir":
		return true
	default:
		return false
	}
}

func pragmaAllowed(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "table_list", "table_xinfo", "index_list", "index_xinfo", "foreign_key_list":
		return true
	default:
		return false
	}
}

func configureConnection(conn rawConnection, mode accessMode) error {
	defensive, err := conn.Config(sqlite3.DBCONFIG_DEFENSIVE, true)
	if err != nil {
		return fmt.Errorf("enabling sqlite defensive mode: %w", err)
	}

	if !defensive {
		return fmt.Errorf("enabling sqlite defensive mode: %w", errSQLiteFileUnavailable)
	}

	trusted, err := conn.Config(sqlite3.DBCONFIG_TRUSTED_SCHEMA, false)
	if err != nil {
		return fmt.Errorf("disabling sqlite trusted schema: %w", err)
	}

	if trusted {
		return fmt.Errorf("disabling sqlite trusted schema: %w", errSQLiteFileUnavailable)
	}

	if err := conn.Exec("PRAGMA query_only=ON"); err != nil {
		return fmt.Errorf("enabling sqlite query-only mode: %w", err)
	}

	if err := validateSQLiteConnection(conn); err != nil {
		return err
	}

	if err := conn.SetAuthorizer(func(
		action sqlite3.AuthorizerActionCode,
		name3rd string,
		name4th string,
		schema string,
		inner string,
	) sqlite3.AuthorizerReturnCode {
		return authorize(mode, action, name3rd, name4th, schema, inner)
	}); err != nil {
		return fmt.Errorf("installing sqlite authorizer: %w", err)
	}

	return nil
}
