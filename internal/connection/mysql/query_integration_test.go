//go:build integration

package mysql

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
)

func newMySQLIntegrationQueryExecutor(t *testing.T, opener *Opener, options QueryOptions) *QueryExecutor {
	t.Helper()

	executor, err := NewQueryExecutor(opener, options)
	if err != nil {
		t.Fatalf("NewQueryExecutor() error = %v", err)
	}

	return executor
}

func queryRequest(id connection.ID, query string) execution.RelationalQueryExecutionRequest {
	return execution.RelationalQueryExecutionRequest{
		Source: connection.Definition{ID: id, Kind: Kind},
		Query:  query,
	}
}

func TestQueryMySQLIntegration(t *testing.T) {
	t.Parallel()

	fixture := newMySQLIntegrationFixture(t)
	createMySQLDiscoveryFixture(t, fixture)
	primary := testQuotedIdentifier(t, fixture.primaryDB)
	testExecSQL(t, fixture.admin, fmt.Sprintf(
		"INSERT INTO %s.accounts (external_id,balance,state,binary_fixed,binary_var,binary_blob) VALUES ('a',1,'active',X'00FF',X'00FF',X'00FF'),('b',2,'active',X'0102',X'0102',X'0102')",
		primary,
	))

	opener := newMySQLIntegrationOpener(t, "mysql_primary", fixture.readerURI(t, fixture.primaryDB, fixture.password))
	executor := newMySQLIntegrationQueryExecutor(t, opener, QueryOptions{
		Timeout: 2 * time.Second, ResponseByteLimit: 64 * 1024,
		TruncationEnabled: true, RowLimit: 100,
	})

	for _, query := range []string{
		"SELECT id, external_id FROM accounts ORDER BY id",
		"WITH selected AS (SELECT id FROM accounts) SELECT id FROM selected ORDER BY id",
	} {
		result, err := executor.Query(t.Context(), queryRequest("mysql_primary", query))
		if err != nil || result.RowCount == 0 {
			t.Fatalf("read query=%q result=%#v error=%v", query, result, err)
		}
	}

	for _, query := range []string{
		"INSERT INTO accounts (external_id, balance, state) VALUES ('blocked', 1, 'active')",
		"CREATE TABLE should_not_exist (id INT)",
		"SELECT 1; SELECT 2",
		"SELECT * FROM definitely_missing_table",
	} {
		_, err := executor.Query(t.Context(), queryRequest("mysql_primary", query))
		if err == nil {
			t.Fatalf("query %q unexpectedly succeeded", query)
		}
	}

	_, err := executor.Query(t.Context(), queryRequest("mysql_primary", "SELECT User FROM mysql.user"))
	if failure := execution.ClassifyRelationalQuery(t.Context(), err); failure.Category != execution.ErrorCategoryDatabasePermissionDenied {
		t.Fatalf("permission failure=%#v", failure)
	}

	binary, err := executor.Query(t.Context(), queryRequest("mysql_primary",
		"SELECT binary_fixed,binary_var,binary_blob FROM accounts WHERE external_id='a'"))
	if err != nil {
		t.Fatalf("binary query error=%v", err)
	}

	if len(binary.Rows) != 1 || len(binary.Rows[0]) != 3 {
		t.Fatalf("binary result=%#v", binary)
	}

	for _, value := range binary.Rows[0] {
		if value == nil || *value != "X'00FF'" {
			t.Fatalf("binary value=%v, want X'00FF'", value)
		}
	}

	if _, err := executor.Query(t.Context(), queryRequest("mysql_primary", "SELECT @dataporch_session_marker := 'dirty'")); err != nil {
		t.Fatalf("setting session marker error=%v", err)
	}

	marker, err := executor.Query(t.Context(), queryRequest("mysql_primary", "SELECT @dataporch_session_marker"))
	if err != nil || len(marker.Rows) != 1 || len(marker.Rows[0]) != 1 || marker.Rows[0][0] != nil {
		t.Fatalf("session marker result=%#v error=%v", marker, err)
	}

	if _, err := executor.Query(t.Context(), queryRequest("mysql_primary", "SELECT 1")); err != nil {
		t.Fatalf("post-isolation SELECT error=%v", err)
	}

	truncated := newMySQLIntegrationQueryExecutor(t, opener, QueryOptions{
		Timeout: 2 * time.Second, ResponseByteLimit: 64 * 1024,
		TruncationEnabled: true, RowLimit: 1,
	})

	result, err := truncated.Query(t.Context(), queryRequest("mysql_primary", "SELECT id FROM accounts ORDER BY id"))
	if err != nil || result.RowCount != 1 || !result.Truncated {
		t.Fatalf("truncated result=%#v error=%v", result, err)
	}

	tiny := newMySQLIntegrationQueryExecutor(t, opener, QueryOptions{
		Timeout: 2 * time.Second, ResponseByteLimit: 64,
	})

	_, err = tiny.Query(t.Context(), queryRequest("mysql_primary", "SELECT REPEAT('x', 1024)"))
	if !errors.Is(err, execution.ErrResultTooLarge) {
		t.Fatalf("byte-limit error=%v, want %v", err, execution.ErrResultTooLarge)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = executor.Query(cancelled, queryRequest("mysql_primary", "SELECT 1"))
	if !errors.Is(err, execution.ErrCancelled) {
		t.Fatalf("cancelled query error=%v", err)
	}

	deadline, stop := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer stop()

	_, err = executor.Query(deadline, queryRequest("mysql_primary", "SELECT SLEEP(1)"))
	if !errors.Is(err, execution.ErrQueryTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline query error=%v", err)
	}
}
