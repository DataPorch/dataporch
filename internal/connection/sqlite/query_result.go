package sqlite

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/DataPorch/dataporch/internal/execution"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

func (e *QueryExecutor) readResult(
	queryContext context.Context,
	stmt statement,
	result execution.RelationalQueryResult,
) (execution.RelationalQueryResult, error) {
	if stmt == nil {
		return execution.RelationalQueryResult{}, execution.ErrInvalidQuery
	}

	columnCount := stmt.ColumnCount()
	if columnCount == 0 {
		return execution.RelationalQueryResult{}, execution.ErrInvalidQuery
	}

	result.Columns = make([]execution.RelationalQueryColumn, 0, columnCount)
	for index := range columnCount {
		result.Columns = append(result.Columns, execution.RelationalQueryColumn{
			Name:         stmt.ColumnName(index),
			DatabaseType: stmt.ColumnDeclType(index),
		})
	}

	result.Rows = make([][]*string, 0)

	budget, err := newQueryResultBudget(result, e.byteLimit)
	if err != nil {
		return execution.RelationalQueryResult{}, err
	}

	if err := e.readRows(queryContext, stmt, &result, &budget); err != nil {
		return execution.RelationalQueryResult{}, err
	}

	result.RowCount = len(result.Rows)

	return result, nil
}

func (e *QueryExecutor) readRows(
	queryContext context.Context,
	stmt statement,
	result *execution.RelationalQueryResult,
	budget *queryResultBudget,
) error {
	for stmt.Step() {
		if err := queryContext.Err(); err != nil {
			return err
		}

		if e.truncate && len(result.Rows) == e.rowLimit {
			result.Truncated = true
			break
		}

		row, err := e.readRow(stmt, len(result.Columns))
		if err != nil {
			return err
		}

		rowSize, rowFits, err := encodedRowSize(row, e.byteLimit)
		if err != nil {
			return err
		}

		if !rowFits || !budget.FitsAdditionalRow(rowSize) {
			return execution.ErrResultTooLarge
		}

		result.Rows = append(result.Rows, row)

		budget.RetainRow(rowSize)
	}

	if err := stmt.Err(); err != nil {
		return err
	}

	if err := queryContext.Err(); err != nil {
		return err
	}

	return nil
}

func (e *QueryExecutor) readRow(stmt statement, columnCount int) ([]*string, error) {
	row := make([]*string, columnCount)
	for index := range columnCount {
		value, err := e.cellValue(stmt, index)
		if err != nil {
			return nil, err
		}

		row[index] = value
	}

	return row, nil
}

func (e *QueryExecutor) cellValue(stmt statement, index int) (*string, error) {
	switch stmt.ColumnType(index) {
	case sqlite3.NULL:
		return nil, nil
	case sqlite3.INTEGER:
		value := strconv.FormatInt(stmt.ColumnInt64(index), 10)
		return &value, nil
	case sqlite3.FLOAT:
		value := strconv.FormatFloat(stmt.ColumnFloat(index), 'g', -1, 64)
		return &value, nil
	case sqlite3.TEXT:
		raw := stmt.ColumnRawText(index)
		if len(raw) > e.byteLimit {
			return nil, execution.ErrResultTooLarge
		}

		value := string(raw)

		return &value, nil
	case sqlite3.BLOB:
		raw := stmt.ColumnRawBlob(index)
		if e.byteLimit < 3 || len(raw) > (e.byteLimit-3)/2 {
			return nil, execution.ErrResultTooLarge
		}

		value := blobLiteral(raw)

		return &value, nil
	default:
		return nil, execution.ErrInternal
	}
}

func blobLiteral(raw []byte) string {
	const digits = "0123456789ABCDEF"

	encoded := make([]byte, 3+2*len(raw))
	encoded[0] = 'X'

	encoded[1] = '\''
	for index, value := range raw {
		encoded[2+2*index] = digits[value>>4]
		encoded[3+2*index] = digits[value&0x0f]
	}

	encoded[len(encoded)-1] = '\''

	return string(encoded)
}

type queryResultBudget struct {
	limit        int
	fixedSize    int
	rowsSize     int
	retainedRows int
}

func newQueryResultBudget(result execution.RelationalQueryResult, limit int) (queryResultBudget, error) {
	kindJSON, err := json.Marshal(result.Kind)
	if err != nil {
		return queryResultBudget{}, err
	}

	sourceJSON, err := json.Marshal(result.SourceID)
	if err != nil {
		return queryResultBudget{}, err
	}

	columnsJSON, err := json.Marshal(result.Columns)
	if err != nil {
		return queryResultBudget{}, err
	}

	fixedSize := len(`{"kind":`) + len(kindJSON) +
		len(`,"source_id":`) + len(sourceJSON) +
		len(`,"columns":`) + len(columnsJSON) +
		len(`,"rows":`) +
		len(`,"row_count":`) +
		len(`,"truncated":`) +
		len(`}`)

	budget := queryResultBudget{
		limit:     limit,
		fixedSize: fixedSize,
		rowsSize:  len(`[]`),
	}
	if !budget.fits(
		budget.fixedSize,
		budget.rowsSize,
		len(strconv.Itoa(0)),
		len(strconv.FormatBool(false)),
	) {
		return queryResultBudget{}, execution.ErrResultTooLarge
	}

	return budget, nil
}

func (b queryResultBudget) fits(parts ...int) bool {
	remaining := b.limit
	for _, part := range parts {
		if part < 0 || part > remaining {
			return false
		}

		remaining -= part
	}

	return true
}

func (b queryResultBudget) FitsAdditionalRow(rowSize int) bool {
	separator := 0
	if b.retainedRows > 0 {
		separator = 1
	}

	nextCount := b.retainedRows + 1

	return b.fits(
		b.fixedSize,
		b.rowsSize,
		separator,
		rowSize,
		len(strconv.Itoa(nextCount)),
		len(strconv.FormatBool(false)),
	)
}

func (b *queryResultBudget) RetainRow(rowSize int) {
	if b.retainedRows > 0 {
		b.rowsSize++
	}

	b.rowsSize += rowSize
	b.retainedRows++
}

func encodedRowSize(row []*string, limit int) (int, bool, error) {
	remaining := limit

	consume := func(size int) bool {
		if size < 0 || size > remaining {
			return false
		}

		remaining -= size

		return true
	}
	if !consume(2) {
		return 0, false, nil
	}

	for index, value := range row {
		if index > 0 && !consume(1) {
			return 0, false, nil
		}

		if value == nil {
			if !consume(len("null")) {
				return 0, false, nil
			}

			continue
		}

		encoded, err := json.Marshal(*value)
		if err != nil {
			return 0, false, err
		}

		if !consume(len(encoded)) {
			return 0, false, nil
		}
	}

	return limit - remaining, true, nil
}
