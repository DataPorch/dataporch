package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/DataPorch/dataporch/internal/execution"
	"github.com/jackc/pgx/v5/pgconn"
)

func (e *QueryExecutor) readResult(
	queryContext context.Context,
	connection queryConnection,
	rows queryRows,
	request execution.RelationalQueryExecutionRequest,
) (result execution.RelationalQueryResult, returnErr error) {
	if rows == nil {
		return result, execution.ErrInternal
	}

	fields := rows.FieldDescriptions()
	if len(fields) == 0 {
		rows.Close()

		if terminalErr := rows.Err(); terminalErr != nil {
			return result, terminalErr
		}

		return result, execution.ErrInvalidQuery
	}

	defer func() {
		rows.Close()

		if terminalErr := rows.Err(); terminalErr != nil {
			result = execution.RelationalQueryResult{}
			returnErr = errors.Join(returnErr, terminalErr)
		}
	}()

	result = execution.RelationalQueryResult{
		Kind:     request.Source.Kind,
		SourceID: request.Source.ID,
		Columns:  queryColumns(connection, fields),
		Rows:     make([][]*string, 0, initialQueryRowCapacity(e)),
	}

	return e.readRows(queryContext, rows, result)
}

func (e *QueryExecutor) readRows(
	queryContext context.Context,
	rows queryRows,
	result execution.RelationalQueryResult,
) (execution.RelationalQueryResult, error) {
	budget, err := newQueryResultBudget(result, e.byteLimit)
	if err != nil {
		return execution.RelationalQueryResult{}, err
	}

	for rows.Next() {
		if err := queryContext.Err(); err != nil {
			return execution.RelationalQueryResult{}, err
		}

		if e.truncate && len(result.Rows) == e.rowLimit {
			result.Truncated = true
			break
		}

		rawValues := rows.RawValues()
		if len(rawValues) != len(result.Columns) {
			return execution.RelationalQueryResult{}, execution.ErrInternal
		}

		rowSize, rowFits, err := encodedRawRowSize(rawValues, e.byteLimit)
		if err != nil {
			return execution.RelationalQueryResult{}, execution.ErrInternal
		}

		if !rowFits || !budget.FitsAdditionalRow(rowSize) {
			return execution.RelationalQueryResult{}, execution.ErrResultTooLarge
		}

		row := make([]*string, len(rawValues))
		for index, rawValue := range rawValues {
			if rawValue == nil {
				continue
			}

			value := string(append([]byte(nil), rawValue...))
			row[index] = &value
		}

		result.Rows = append(result.Rows, row)
		result.RowCount = len(result.Rows)

		budget.RetainRow(rowSize)
	}

	if err := queryContext.Err(); err != nil {
		return execution.RelationalQueryResult{}, err
	}

	result.RowCount = len(result.Rows)

	return result, nil
}

func queryColumns(
	connection queryConnection,
	fields []pgconn.FieldDescription,
) []execution.RelationalQueryColumn {
	columns := make([]execution.RelationalQueryColumn, 0, len(fields))
	for _, field := range fields {
		databaseType, ok := connection.DatabaseTypeName(field.DataTypeOID)
		if !ok {
			databaseType = "oid:" + strconv.FormatUint(uint64(field.DataTypeOID), 10)
		}

		columns = append(columns, execution.RelationalQueryColumn{
			Name:         field.Name,
			DatabaseType: databaseType,
		})
	}

	return columns
}

func initialQueryRowCapacity(executor *QueryExecutor) int {
	if !executor.truncate {
		return 0
	}

	return min(executor.rowLimit, 1000)
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

func encodedRawRowSize(rawValues [][]byte, limit int) (int, bool, error) {
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

	for index, rawValue := range rawValues {
		if index > 0 && !consume(1) {
			return 0, false, nil
		}

		if rawValue == nil {
			if !consume(len("null")) {
				return 0, false, nil
			}

			continue
		}

		if remaining < 2 || len(rawValue) > remaining-2 {
			return 0, false, nil
		}

		encoded, err := json.Marshal(string(rawValue))
		if err != nil {
			return 0, false, err
		}

		if !consume(len(encoded)) {
			return 0, false, nil
		}
	}

	return limit - remaining, true, nil
}
