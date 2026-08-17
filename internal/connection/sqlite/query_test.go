package sqlite

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

func TestNewQueryExecutorValidatesDependenciesAndOptions(t *testing.T) {
	t.Parallel()

	valid := QueryOptions{
		Timeout:           time.Second,
		ResponseByteLimit: 1024,
		TruncationEnabled: true,
		RowLimit:          1,
	}
	tests := []struct {
		name    string
		opener  queryOpener
		options QueryOptions
		want    error
	}{
		{name: "nil runtime", options: valid, want: errQueryOpenerRequired},
		{name: "timeout", opener: &queryOpenerStub{}, options: QueryOptions{ResponseByteLimit: 1}, want: errQueryTimeoutRequired},
		{name: "byte limit", opener: &queryOpenerStub{}, options: QueryOptions{Timeout: time.Second}, want: errQueryByteLimitRequired},
		{name: "row limit", opener: &queryOpenerStub{}, options: QueryOptions{Timeout: time.Second, ResponseByteLimit: 1, TruncationEnabled: true}, want: errQueryRowLimitRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewQueryExecutor(test.opener, test.options)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewQueryExecutor() error = %v, want %v", err, test.want)
			}
		})
	}

	if _, err := NewQueryExecutor(&queryOpenerStub{}, valid); err != nil {
		t.Fatalf("NewQueryExecutor(valid) error = %v", err)
	}
}

func TestQueryExecutorPassesOriginalQueryUnchangedAndAcceptsShapes(t *testing.T) {
	t.Parallel()

	queries := []string{
		"SELECT 1",
		"WITH values_cte AS (SELECT 1 AS value) SELECT value FROM values_cte",
		"VALUES (1), (2)",
		"EXPLAIN SELECT 1",
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			stmt := newQueryStatement([]queryRow{{cells: []queryCell{{kind: sqlite3.INTEGER, integer: 1}}}})
			opener := &queryOpenerStub{conn: &queryRawConnection{stmt: stmt}}
			openerRaw := opener.conn.(*queryRawConnection)
			openerRaw.queryLog = &opener.queries
			executor := newTestQueryExecutor(t, opener, QueryOptions{
				Timeout:           time.Second,
				ResponseByteLimit: 1024,
			})

			result, err := executor.Query(t.Context(), queryRequest(query))
			if err != nil {
				t.Fatalf("Query() error = %v", err)
			}
			if !reflect.DeepEqual(opener.queries, []string{query}) {
				t.Fatalf("Prepare queries = %#v, want exact original query", opener.queries)
			}
			if result.RowCount != 1 || len(result.Columns) != 1 {
				t.Fatalf("result = %#v, want one-column one-row result", result)
			}
		})
	}
}

func TestQueryExecutorRejectsInvalidStatementShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		stmt  *queryStatement
		tail  string
		bind  int
		cols  int
		query string
	}{
		{name: "empty", query: "   -- comment only\n"},
		{name: "multiple statements", stmt: newQueryStatement(nil), tail: "SELECT 2", query: "SELECT 1; SELECT 2"},
		{name: "parameters", stmt: newQueryStatement(nil), bind: 1, query: "SELECT ?1"},
		{name: "no columns", stmt: newQueryStatement(nil), cols: 0, query: "UPDATE items SET value = 1"},
		{name: "nil statement", query: "SELECT 1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stmt := test.stmt
			if stmt != nil {
				stmt.bindCount = test.bind
				stmt.columnCount = test.cols
				if test.cols == 0 && test.bind == 0 {
					stmt.columnCount = 0
				}
			}
			opener := &queryOpenerStub{conn: &queryRawConnection{stmt: stmt, tail: test.tail}}
			executor := newTestQueryExecutor(t, opener, QueryOptions{
				Timeout:           time.Second,
				ResponseByteLimit: 1024,
			})
			_, err := executor.Query(t.Context(), queryRequest(test.query))
			if !errors.Is(err, execution.ErrInvalidQuery) {
				t.Fatalf("Query() error = %v, want ErrInvalidQuery", err)
			}
		})
	}
}

func TestQueryExecutorPreservesStorageClassesAndNulls(t *testing.T) {
	t.Parallel()

	stmt := newQueryStatement([]queryRow{{cells: []queryCell{
		{kind: sqlite3.INTEGER, integer: -9223372036854775808},
		{kind: sqlite3.FLOAT, float: 1.25},
		{kind: sqlite3.TEXT, text: "null"},
		{kind: sqlite3.TEXT, text: ""},
		{kind: sqlite3.BLOB, blob: []byte{0x00, 0xab, 0xff}},
		{kind: sqlite3.BLOB, blob: []byte{}},
		{kind: sqlite3.NULL},
	}}})
	stmt.columnNames = []string{"value", "value", "text", "empty", "blob", "empty_blob", "null"}
	stmt.columnDeclTypes = []string{"INTEGER", "REAL", "TEXT", "TEXT", "BLOB", "BLOB", ""}
	opener := &queryOpenerStub{conn: &queryRawConnection{stmt: stmt}}
	executor := newTestQueryExecutor(t, opener, QueryOptions{
		Timeout:           time.Second,
		ResponseByteLimit: 1024,
	})

	result, err := executor.Query(t.Context(), queryRequest("SELECT values"))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	want := [][]*string{{
		stringPtr("-9223372036854775808"),
		stringPtr("1.25"),
		stringPtr("null"),
		stringPtr(""),
		stringPtr("X'00ABFF'"),
		stringPtr("X''"),
		nil,
	}}
	if !reflect.DeepEqual(result.Rows, want) {
		for rowIndex, row := range result.Rows {
			for columnIndex, value := range row {
				if value == nil {
					t.Logf("got row[%d][%d] = nil", rowIndex, columnIndex)
				} else {
					t.Logf("got row[%d][%d] = %q", rowIndex, columnIndex, *value)
				}
			}
		}
		t.Fatalf("rows = %#v, want %#v", result.Rows, want)
	}
	if result.Columns[0].Name != "value" || result.Columns[1].Name != "value" || result.Columns[4].DatabaseType != "BLOB" {
		t.Fatalf("columns = %#v, want duplicate names and declaration types", result.Columns)
	}
}

func TestQueryExecutorTruncatesWithOneRowLookahead(t *testing.T) {
	t.Parallel()

	stmt := newQueryStatement([]queryRow{
		{cells: []queryCell{{kind: sqlite3.INTEGER, integer: 1}}},
		{cells: []queryCell{{kind: sqlite3.INTEGER, integer: 2}}},
		{cells: []queryCell{{kind: sqlite3.INTEGER, integer: 3}}},
	})
	opener := &queryOpenerStub{conn: &queryRawConnection{stmt: stmt}}
	executor := newTestQueryExecutor(t, opener, QueryOptions{
		Timeout:           time.Second,
		ResponseByteLimit: 1024,
		TruncationEnabled: true,
		RowLimit:          2,
	})

	result, err := executor.Query(t.Context(), queryRequest("SELECT value"))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(result.Rows) != 2 || result.RowCount != 2 || !result.Truncated {
		t.Fatalf("result = %#v, want two retained rows and truncation", result)
	}
}

func TestQueryExecutorReadsRealSQLiteRows(t *testing.T) {
	t.Parallel()

	path := createQueryFixture(t)
	runtime, err := NewRuntime(&fixturePreparer{path: path})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	executor, err := NewQueryExecutor(runtime, QueryOptions{
		Timeout:           time.Second,
		ResponseByteLimit: 4096,
	})
	if err != nil {
		t.Fatalf("NewQueryExecutor() error = %v", err)
	}

	result, err := executor.Query(t.Context(), queryRequest("SELECT id, value FROM query_items ORDER BY id"))
	if err != nil {
		t.Fatalf("Query(real) error = %v", err)
	}
	if got := result.Columns; !reflect.DeepEqual(got, []execution.RelationalQueryColumn{{Name: "id", DatabaseType: "INTEGER"}, {Name: "value", DatabaseType: "TEXT"}}) {
		t.Fatalf("columns = %#v, want declared SQLite types", got)
	}
	if len(result.Rows) != 2 || result.Rows[0][0] == nil || *result.Rows[0][0] != "1" || result.Rows[0][1] == nil || *result.Rows[0][1] != "one" {
		t.Fatalf("rows = %#v, want integer/text values", result.Rows)
	}
}

func TestQueryExecutorRejectsWritesWithoutChangingFile(t *testing.T) {
	t.Parallel()

	path := createQueryFixture(t)
	runtime, err := NewRuntime(&fixturePreparer{path: path})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	executor, err := NewQueryExecutor(runtime, QueryOptions{
		Timeout:           time.Second,
		ResponseByteLimit: 1024,
	})
	if err != nil {
		t.Fatalf("NewQueryExecutor() error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(before) error = %v", err)
	}

	queries := []string{
		"INSERT INTO query_items(value) VALUES ('changed')",
		"UPDATE query_items SET value = 'changed'",
		"DELETE FROM query_items",
		"CREATE TABLE created_by_query(value TEXT)",
		"INSERT INTO query_items(value) VALUES ('changed') RETURNING value",
		"BEGIN",
		"SAVEPOINT blocked",
		"ATTACH DATABASE 'other.db' AS other",
		"DETACH DATABASE other",
		"PRAGMA query_only",
		"SELECT * FROM pragma_table_list",
		"SELECT load_extension('x')",
		"SELECT readfile('x')",
		"SELECT writefile('x', 'x')",
		"SELECT 1; SELECT 2",
		"SELECT ?1",
	}
	for _, query := range queries {
		if _, err := executor.Query(t.Context(), queryRequest(query)); err == nil {
			t.Errorf("Query(%q) error = nil, want rejection", query)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected queries changed the SQLite file")
	}
}

func TestQueryExecutorHonorsCallerCancellation(t *testing.T) {
	t.Parallel()

	executor := newTestQueryExecutor(t, &queryOpenerStub{}, QueryOptions{
		Timeout:           time.Second,
		ResponseByteLimit: 1024,
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := executor.Query(ctx, queryRequest("SELECT 1"))
	if !errors.Is(err, execution.ErrCancelled) {
		t.Fatalf("Query(cancelled) error = %v, want ErrCancelled", err)
	}
}

func newTestQueryExecutor(t *testing.T, opener queryOpener, options QueryOptions) *QueryExecutor {
	t.Helper()

	executor, err := NewQueryExecutor(opener, options)
	if err != nil {
		t.Fatalf("NewQueryExecutor() error = %v", err)
	}
	return executor
}

func queryRequest(query string) execution.RelationalQueryExecutionRequest {
	return execution.RelationalQueryExecutionRequest{
		Source: executionSource(),
		Query:  query,
	}
}

func executionSource() connection.Definition {
	return connection.Definition{ID: "query-source", Kind: Kind}
}

type queryOpenerStub struct {
	conn    rawConnection
	queries []string
	mode    accessMode
}

func (o *queryOpenerStub) open(_ context.Context, _ connection.ID, mode accessMode) (*client, error) {
	o.mode = mode
	if o.conn == nil {
		return nil, errors.New("query opener stub")
	}
	return &client{conn: o.conn, release: func() {}}, nil
}

type queryRawConnection struct {
	stmt     *queryStatement
	tail     string
	queryLog *[]string
}

func (c *queryRawConnection) Close() error                                 { return nil }
func (*queryRawConnection) Config(sqlite3.DBConfig, ...bool) (bool, error) { return true, nil }
func (*queryRawConnection) Exec(string) error                              { return nil }
func (c *queryRawConnection) Prepare(sql string) (statement, string, error) {
	if c.queryLog != nil {
		*c.queryLog = append(*c.queryLog, sql)
	}
	if c.stmt != nil {
		c.stmt.sql = sql
	}
	return c.stmt, c.tail, nil
}
func (*queryRawConnection) SetAuthorizer(func(sqlite3.AuthorizerActionCode, string, string, string, string) sqlite3.AuthorizerReturnCode) error {
	return nil
}
func (*queryRawConnection) SetInterrupt(context.Context) context.Context { return context.Background() }

type queryRow struct {
	cells []queryCell
}

type queryCell struct {
	kind    sqlite3.Datatype
	text    string
	integer int64
	float   float64
	blob    []byte
}

type queryStatement struct {
	sql             string
	bindCount       int
	columnCount     int
	columnNames     []string
	columnDeclTypes []string
	rows            []queryRow
	current         int
	stepErr         error
	closeErr        error
}

func newQueryStatement(rows []queryRow) *queryStatement {
	columns := 0
	if len(rows) > 0 {
		columns = len(rows[0].cells)
	}
	return &queryStatement{
		columnCount:     columns,
		columnNames:     make([]string, columns),
		columnDeclTypes: make([]string, columns),
		rows:            rows,
		current:         -1,
	}
}

func (s *queryStatement) BindCount() int           { return s.bindCount }
func (*queryStatement) BindInt64(int, int64) error { return nil }
func (*queryStatement) BindText(int, string) error { return nil }
func (s *queryStatement) Close() error             { return s.closeErr }
func (s *queryStatement) ColumnCount() int         { return s.columnCount }
func (s *queryStatement) ColumnDeclType(index int) string {
	if index < len(s.columnDeclTypes) {
		return s.columnDeclTypes[index]
	}
	return ""
}
func (s *queryStatement) ColumnFloat(index int) float64 { return s.rows[s.current].cells[index].float }
func (s *queryStatement) ColumnInt64(index int) int64   { return s.rows[s.current].cells[index].integer }
func (s *queryStatement) ColumnName(index int) string {
	if index < len(s.columnNames) && s.columnNames[index] != "" {
		return s.columnNames[index]
	}
	return "column"
}
func (s *queryStatement) ColumnRawBlob(index int) []byte { return s.rows[s.current].cells[index].blob }
func (s *queryStatement) ColumnRawText(index int) []byte {
	return []byte(s.rows[s.current].cells[index].text)
}
func (s *queryStatement) ColumnText(index int) string { return s.rows[s.current].cells[index].text }
func (s *queryStatement) ColumnType(index int) sqlite3.Datatype {
	return s.rows[s.current].cells[index].kind
}
func (s *queryStatement) Err() error { return s.stepErr }
func (s *queryStatement) Step() bool {
	if s.current+1 >= len(s.rows) {
		return false
	}
	s.current++
	return true
}
func createQueryFixture(t *testing.T) string {
	t.Helper()

	path := t.TempDir() + "/query.db"
	conn, err := sqlite3.OpenFlags(path, sqlite3.OPEN_READWRITE|sqlite3.OPEN_CREATE|sqlite3.OPEN_URI)
	if err != nil {
		t.Fatalf("OpenFlags(query) error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, statement := range []string{
		`CREATE TABLE query_items (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO query_items(value) VALUES ('one'), ('two')`,
	} {
		if err := conn.Exec(statement); err != nil {
			t.Fatalf("fixture Exec(%q) error = %v", statement, err)
		}
	}
	return path
}
