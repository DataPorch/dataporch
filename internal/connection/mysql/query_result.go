package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/adamraziv/dataporch/internal/execution"
)

type queryResultReader struct {
	responseByteLimit int
	truncationEnabled bool
	rowLimit          int
}

func mysqlCellString(value any, databaseType string) (*string, error) {
	switch value := value.(type) {
	case nil:
		return nil, nil
	case []byte:
		raw := append([]byte(nil), value...)
		if isBinaryDatabaseType(databaseType) {
			maxInt := int(^uint(0) >> 1)
			if len(raw) > (maxInt-3)/2 {
				return nil, execution.ErrResultTooLarge
			}
			text := mysqlBinaryLiteral(raw)
			return &text, nil
		}
		text := string(raw)
		return &text, nil
	case string:
		text := strings.Clone(value)
		return &text, nil
	case int64:
		text := strconv.FormatInt(value, 10)
		return &text, nil
	case uint64:
		text := strconv.FormatUint(value, 10)
		return &text, nil
	case float64:
		text := strconv.FormatFloat(value, 'g', -1, 64)
		return &text, nil
	case bool:
		text := strconv.FormatBool(value)
		return &text, nil
	case time.Time:
		text := value.UTC().Format(time.RFC3339Nano)
		return &text, nil
	default:
		return nil, execution.ErrInternal
	}
}

func isBinaryDatabaseType(databaseType string) bool {
	switch strings.ToUpper(strings.TrimSpace(databaseType)) {
	case "BINARY", "VARBINARY", "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB", "BIT", "GEOMETRY", "VECTOR":
		return true
	default:
		return false
	}
}

func mysqlBinaryLiteral(raw []byte) string {
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

func newQueryResultBudget(
	result execution.RelationalQueryResult,
	limit int,
) (queryResultBudget, error) {
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

func (r queryResultReader) readResult(
	ctx context.Context,
	rows queryRows,
	request execution.RelationalQueryExecutionRequest,
) (result execution.RelationalQueryResult, returnErr error) {
	if ctx == nil || rows == nil {
		return result, execution.ErrInternal
	}

	defer func() {
		closeErr := rows.Close()
		terminalErr := rows.Err()
		if closeErr != nil || terminalErr != nil {
			result = execution.RelationalQueryResult{}
			returnErr = errors.Join(returnErr, closeErr, terminalErr)
		}
	}()

	names, err := rows.Columns()
	if err != nil {
		return result, err
	}
	if len(names) == 0 {
		return result, execution.ErrInvalidQuery
	}

	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return result, err
	}
	if len(columnTypes) != len(names) {
		return result, execution.ErrInternal
	}

	result = execution.RelationalQueryResult{
		Kind:     Kind,
		SourceID: request.Source.ID,
		Columns:  make([]execution.RelationalQueryColumn, len(names)),
		Rows:     make([][]*string, 0),
	}
	for index, name := range names {
		result.Columns[index] = execution.RelationalQueryColumn{
			Name:         name,
			DatabaseType: columnTypes[index].DatabaseTypeName(),
		}
	}

	budget, err := newQueryResultBudget(result, r.responseByteLimit)
	if err != nil {
		return execution.RelationalQueryResult{}, err
	}

	raw := make([]any, len(names))
	destinations := make([]any, len(names))
	for index := range raw {
		destinations[index] = &raw[index]
	}

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return execution.RelationalQueryResult{}, err
		}
		if r.truncationEnabled && budget.retainedRows >= r.rowLimit {
			result.Truncated = true
			break
		}

		clear(raw)
		if err := rows.Scan(destinations...); err != nil {
			return execution.RelationalQueryResult{}, err
		}

		row := make([]*string, len(raw))
		for index, value := range raw {
			databaseType := columnTypes[index].DatabaseTypeName()
			switch typed := value.(type) {
			case []byte:
				if isBinaryDatabaseType(databaseType) {
					if r.responseByteLimit < 3 || len(typed) > (r.responseByteLimit-3)/2 {
						return execution.RelationalQueryResult{}, execution.ErrResultTooLarge
					}
				} else if len(typed) > r.responseByteLimit {
					return execution.RelationalQueryResult{}, execution.ErrResultTooLarge
				}
			case string:
				if len(typed) > r.responseByteLimit {
					return execution.RelationalQueryResult{}, execution.ErrResultTooLarge
				}
			}

			cell, err := mysqlCellString(value, databaseType)
			if err != nil {
				return execution.RelationalQueryResult{}, err
			}
			row[index] = cell
		}

		rowSize, fits, err := encodedRowSize(row, r.responseByteLimit)
		if err != nil {
			return execution.RelationalQueryResult{}, err
		}
		if !fits || !budget.FitsAdditionalRow(rowSize) {
			return execution.RelationalQueryResult{}, execution.ErrResultTooLarge
		}
		budget.RetainRow(rowSize)
		result.Rows = append(result.Rows, row)
	}

	if err := ctx.Err(); err != nil {
		return execution.RelationalQueryResult{}, err
	}
	result.RowCount = len(result.Rows)
	return result, nil
}
