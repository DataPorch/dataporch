package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
)

func TestMySQLCellString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		value        any
		databaseType string
		want         *string
		wantErr      error
	}{
		{name: "null", value: nil, databaseType: "VARCHAR", want: nil},
		{name: "text bytes", value: []byte("hello"), databaseType: "VARCHAR", want: ptr("hello")},
		{name: "text string", value: "hello", databaseType: "TEXT", want: ptr("hello")},
		{name: "binary", value: []byte{0x00, 0xff}, databaseType: "BINARY", want: ptr("X'00FF'")},
		{name: "varbinary", value: []byte{0x00, 0xff}, databaseType: "VARBINARY", want: ptr("X'00FF'")},
		{name: "blob", value: []byte{0x00, 0xff}, databaseType: "BLOB", want: ptr("X'00FF'")},
		{name: "bit", value: []byte{0x80}, databaseType: "BIT", want: ptr("X'80'")},
		{name: "geometry", value: []byte{0x01, 0x02}, databaseType: "GEOMETRY", want: ptr("X'0102'")},
		{name: "vector", value: []byte{0x01, 0x02}, databaseType: "VECTOR", want: ptr("X'0102'")},
		{name: "int64", value: int64(-12), databaseType: "BIGINT", want: ptr("-12")},
		{name: "uint64", value: uint64(12), databaseType: "BIGINT UNSIGNED", want: ptr("12")},
		{name: "float64", value: 1.25, databaseType: "DOUBLE", want: ptr("1.25")},
		{name: "bool", value: true, databaseType: "BOOLEAN", want: ptr("true")},
		{name: "time", value: time.Date(2026, 8, 18, 1, 2, 3, 4, time.FixedZone("x", 7*60*60)), databaseType: "DATETIME", want: ptr("2026-08-17T18:02:03.000000004Z")},
		{name: "unsupported", value: struct{}{}, databaseType: "UNKNOWN", wantErr: execution.ErrInternal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := mysqlCellString(test.value, test.databaseType)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("mysqlCellString() error = %v, want %v", err, test.wantErr)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("mysqlCellString() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func ptr(value string) *string { return &value }

func TestEncodedRowSize(t *testing.T) {
	t.Parallel()

	row := []*string{ptr("hello"), nil, ptr("X'00FF'")}
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	size, fits, err := encodedRowSize(row, len(encoded))
	if err != nil {
		t.Fatalf("encodedRowSize() error = %v", err)
	}
	if !fits || size != len(encoded) {
		t.Fatalf("size=%d fits=%v, want size=%d fits=true", size, fits, len(encoded))
	}

	_, fits, err = encodedRowSize(row, len(encoded)-1)
	if err != nil {
		t.Fatalf("encodedRowSize() small limit error = %v", err)
	}
	if fits {
		t.Fatal("row must not fit below its encoded JSON size")
	}
}

func TestNewQueryResultBudgetRejectsMetadataOverflow(t *testing.T) {
	t.Parallel()

	result := execution.RelationalQueryResult{
		Kind:     Kind,
		SourceID: "finance",
		Columns: []execution.RelationalQueryColumn{
			{Name: strings.Repeat("x", 128), DatabaseType: "VARCHAR"},
		},
		Rows: make([][]*string, 0),
	}

	_, err := newQueryResultBudget(result, 32)
	if !errors.Is(err, execution.ErrResultTooLarge) {
		t.Fatalf("newQueryResultBudget() error = %v, want %v", err, execution.ErrResultTooLarge)
	}
}

type resultProbeConnector struct{ rows *resultProbeRows }

func (c *resultProbeConnector) Connect(context.Context) (driver.Conn, error) {
	return &resultProbeConn{rows: c.rows}, nil
}

func (*resultProbeConnector) Driver() driver.Driver { return resultProbeDriver{} }

type resultProbeDriver struct{}

func (resultProbeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unexpected Driver.Open")
}

type resultProbeConn struct{ rows *resultProbeRows }

func (*resultProbeConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*resultProbeConn) Close() error                        { return nil }
func (*resultProbeConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (c *resultProbeConn) QueryContext(
	context.Context,
	string,
	[]driver.NamedValue,
) (driver.Rows, error) {
	return c.rows, nil
}

type resultProbeRows struct {
	columns       []string
	databaseTypes []string
	values        [][]driver.Value
	index         int
	terminalErr   error
	closeErr      error
	beforeRow     func()
	closed        atomic.Bool
}

func (r *resultProbeRows) Columns() []string { return append([]string(nil), r.columns...) }

func (r *resultProbeRows) ColumnTypeDatabaseTypeName(index int) string {
	return r.databaseTypes[index]
}

func (r *resultProbeRows) Close() error {
	r.closed.Store(true)
	return r.closeErr
}

func (r *resultProbeRows) Next(destination []driver.Value) error {
	if r.index >= len(r.values) {
		if r.terminalErr != nil {
			err := r.terminalErr
			r.terminalErr = nil
			return err
		}
		return io.EOF
	}
	if r.beforeRow != nil {
		r.beforeRow()
		r.beforeRow = nil
	}
	copy(destination, r.values[r.index])
	r.index++
	return nil
}

var _ driver.QueryerContext = (*resultProbeConn)(nil)
var _ driver.RowsColumnTypeDatabaseTypeName = (*resultProbeRows)(nil)

func openResultProbeRows(t *testing.T, probe *resultProbeRows) *sql.Rows {
	t.Helper()

	db := sql.OpenDB(&resultProbeConnector{rows: probe})
	t.Cleanup(func() { _ = db.Close() })

	rows, err := db.QueryContext(t.Context(), "SELECT probe")
	if err != nil {
		t.Fatalf("QueryContext() error = %v", err)
	}
	return rows
}

func queryResultRequest() execution.RelationalQueryExecutionRequest {
	return execution.RelationalQueryExecutionRequest{
		Source: connection.Definition{ID: "finance", Kind: Kind},
		Query:  "SELECT value",
	}
}

func TestQueryResultReaderRowsAndTruncation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		values        [][]driver.Value
		truncate      bool
		rowLimit      int
		wantRows      int
		wantTruncated bool
	}{
		{name: "exact row count", values: [][]driver.Value{{"a"}}, rowLimit: 1, wantRows: 1},
		{name: "lookahead truncates", values: [][]driver.Value{{"a"}, {"b"}}, truncate: true, rowLimit: 1, wantRows: 1, wantTruncated: true},
		{name: "disabled truncation keeps reading", values: [][]driver.Value{{"a"}, {"b"}}, rowLimit: 1, wantRows: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := &resultProbeRows{
				columns:       []string{"value"},
				databaseTypes: []string{"VARCHAR"},
				values:        test.values,
			}
			reader := queryResultReader{
				responseByteLimit: 4096,
				truncationEnabled: test.truncate,
				rowLimit:          test.rowLimit,
			}
			result, err := reader.readResult(t.Context(), openResultProbeRows(t, probe), queryResultRequest())
			if err != nil {
				t.Fatalf("readResult() error = %v", err)
			}
			if len(result.Rows) != test.wantRows || result.RowCount != test.wantRows || result.Truncated != test.wantTruncated {
				t.Fatalf("result=%#v, want rows=%d truncated=%v", result, test.wantRows, test.wantTruncated)
			}
			if !probe.closed.Load() {
				t.Fatal("rows were not closed")
			}
		})
	}
}

func TestQueryResultReaderRejectsZeroColumns(t *testing.T) {
	t.Parallel()

	probe := &resultProbeRows{}
	reader := queryResultReader{responseByteLimit: 4096}
	_, err := reader.readResult(t.Context(), openResultProbeRows(t, probe), queryResultRequest())
	if !errors.Is(err, execution.ErrInvalidQuery) || !probe.closed.Load() {
		t.Fatalf("readResult() error=%v closed=%v", err, probe.closed.Load())
	}
}

func TestQueryResultReaderBounds(t *testing.T) {
	t.Parallel()

	columns := []execution.RelationalQueryColumn{{Name: "value", DatabaseType: "VARCHAR"}}
	empty := execution.RelationalQueryResult{
		Kind: Kind, SourceID: "finance", Columns: columns, Rows: make([][]*string, 0),
	}
	encodedEmpty, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("Marshal(empty) error = %v", err)
	}

	tests := []struct {
		name         string
		databaseType string
		value        driver.Value
	}{
		{name: "oversized text", databaseType: "VARCHAR", value: strings.Repeat("x", 256)},
		{name: "oversized binary", databaseType: "BLOB", value: []byte(strings.Repeat("x", 256))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := &resultProbeRows{
				columns: []string{"value"}, databaseTypes: []string{test.databaseType},
				values: [][]driver.Value{{test.value}},
			}
			reader := queryResultReader{responseByteLimit: len(encodedEmpty) + 32}
			_, err := reader.readResult(t.Context(), openResultProbeRows(t, probe), queryResultRequest())
			if !errors.Is(err, execution.ErrResultTooLarge) {
				t.Fatalf("readResult() error=%v, want %v", err, execution.ErrResultTooLarge)
			}
		})
	}

	value := strings.Repeat("a", 24)
	one := execution.RelationalQueryResult{
		Kind: Kind, SourceID: "finance", Columns: columns,
		Rows: [][]*string{{ptr(value)}}, RowCount: 1,
	}
	two := execution.RelationalQueryResult{
		Kind: Kind, SourceID: "finance", Columns: columns,
		Rows: [][]*string{{ptr(value)}, {ptr(value)}}, RowCount: 2,
	}
	oneJSON, _ := json.Marshal(one)
	twoJSON, _ := json.Marshal(two)
	limit := (len(oneJSON) + len(twoJSON)) / 2
	probe := &resultProbeRows{
		columns: []string{"value"}, databaseTypes: []string{"VARCHAR"},
		values: [][]driver.Value{{value}, {value}},
	}
	reader := queryResultReader{responseByteLimit: limit}
	_, err = reader.readResult(t.Context(), openResultProbeRows(t, probe), queryResultRequest())
	if !errors.Is(err, execution.ErrResultTooLarge) {
		t.Fatalf("aggregate readResult() error=%v, want %v", err, execution.ErrResultTooLarge)
	}
}

func TestQueryResultReaderReturnsRowsErrorsAndCancellation(t *testing.T) {
	t.Parallel()

	terminalErr := errors.New("terminal rows error")
	probe := &resultProbeRows{
		columns: []string{"value"}, databaseTypes: []string{"VARCHAR"}, terminalErr: terminalErr,
	}
	reader := queryResultReader{responseByteLimit: 4096}
	_, err := reader.readResult(t.Context(), openResultProbeRows(t, probe), queryResultRequest())
	if !errors.Is(err, terminalErr) {
		t.Fatalf("terminal error=%v, want %v", err, terminalErr)
	}

	closeErr := errors.New("rows close error")
	probe = &resultProbeRows{
		columns: []string{"value"}, databaseTypes: []string{"VARCHAR"},
		values: [][]driver.Value{{"a"}}, closeErr: closeErr,
	}
	_, err = reader.readResult(t.Context(), openResultProbeRows(t, probe), queryResultRequest())
	if !errors.Is(err, closeErr) {
		t.Fatalf("close error=%v, want %v", err, closeErr)
	}

	ctx, cancel := context.WithCancel(t.Context())
	probe = &resultProbeRows{
		columns: []string{"value"}, databaseTypes: []string{"VARCHAR"},
		values: [][]driver.Value{{"a"}}, beforeRow: cancel,
	}
	_, err = reader.readResult(ctx, openResultProbeRows(t, probe), queryResultRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read error=%v, want %v", err, context.Canceled)
	}
}
