package sqlite

import (
	"errors"
	"fmt"
	"strings"

	"github.com/adamraziv/dataporch/internal/execution"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

const (
	listPrimaryKeySQL = `
SELECT name, pk
FROM pragma_table_xinfo(?1)
WHERE pk > 0
ORDER BY pk`

	listUniqueIndexesSQL = `
SELECT seq, name, "unique", origin, partial
FROM pragma_index_list(?1)
WHERE "unique" = 1 AND partial = 0
ORDER BY seq`

	listIndexColumnsSQL = `
SELECT seqno, cid, name, "key"
FROM pragma_index_xinfo(?1)
WHERE "key" = 1
ORDER BY seqno`

	listForeignKeysSQL = `
SELECT id, seq, "table", "from", "to", on_update, on_delete, match
FROM pragma_foreign_key_list(?1)
ORDER BY id, seq`
)

const (
	foreignKeyMatchSimple   = "simple"
	foreignKeyMatchPartial  = "partial"
	foreignKeyMatchFull     = "full"
	foreignKeyActionCascade = "cascade"
)

func listSQLiteConstraints(conn rawConnection, table string) ([]execution.Constraint, error) {
	constraints := make([]execution.Constraint, 0)

	primaryColumns, err := listSQLitePrimaryKey(conn, table)
	if err != nil {
		return nil, err
	}

	if len(primaryColumns) > 0 {
		constraints = append(constraints, execution.Constraint{
			Kind:    "primary_key",
			Columns: primaryColumns,
		})
	}

	uniqueConstraints, err := listSQLiteUniqueConstraints(conn, table)
	if err != nil {
		return nil, err
	}

	constraints = append(constraints, uniqueConstraints...)

	foreignConstraints, err := listSQLiteForeignConstraints(conn, table)
	if err != nil {
		return nil, err
	}

	constraints = append(constraints, foreignConstraints...)

	return constraints, nil
}

func listSQLitePrimaryKey(conn rawConnection, table string) (columns []string, retErr error) {
	columns = make([]string, 0)

	stmt, err := prepareSQLiteCatalog(conn, listPrimaryKeySQL, "primary-key catalog")
	if err != nil {
		return columns, err
	}

	defer func() { retErr = errors.Join(retErr, stmt.Close()) }()

	if err := stmt.BindText(1, table); err != nil {
		return columns, fmt.Errorf("sqlite: binding primary-key relation: %w", err)
	}

	for stmt.Step() {
		name := stmt.ColumnText(0)

		pkOrdinal := stmt.ColumnInt64(1)
		if name == "" || pkOrdinal <= 0 {
			return columns, fmt.Errorf("%w: invalid primary-key catalog row", execution.ErrInternal)
		}

		columns = append(columns, name)
	}

	if err := stmt.Err(); err != nil {
		return columns, fmt.Errorf("sqlite: reading primary-key catalog: %w", err)
	}

	return columns, nil
}

func listSQLiteUniqueConstraints(conn rawConnection, table string) (constraints []execution.Constraint, retErr error) {
	constraints = make([]execution.Constraint, 0)

	stmt, err := prepareSQLiteCatalog(conn, listUniqueIndexesSQL, "unique-index catalog")
	if err != nil {
		return constraints, err
	}

	defer func() { retErr = errors.Join(retErr, stmt.Close()) }()

	if err := stmt.BindText(1, table); err != nil {
		return constraints, fmt.Errorf("sqlite: binding unique-index relation: %w", err)
	}

	for stmt.Step() {
		indexName := stmt.ColumnText(1)
		origin := strings.ToLower(strings.TrimSpace(stmt.ColumnText(3)))

		if indexName == "" {
			return constraints, fmt.Errorf("%w: invalid unique-index catalog row", execution.ErrInternal)
		}

		if origin == "pk" {
			continue
		}

		if origin != "c" && origin != "u" {
			continue
		}

		columns, ok, err := listSQLiteIndexColumns(conn, indexName)
		if err != nil {
			return constraints, err
		}

		if !ok {
			continue
		}

		constraints = append(constraints, execution.Constraint{
			Kind:    "unique",
			Columns: columns,
		})
	}

	if err := stmt.Err(); err != nil {
		return constraints, fmt.Errorf("sqlite: reading unique-index catalog: %w", err)
	}

	return constraints, nil
}

func listSQLiteIndexColumns(conn rawConnection, indexName string) (columns []string, valid bool, retErr error) {
	stmt, err := prepareSQLiteCatalog(conn, listIndexColumnsSQL, "index-column catalog")
	if err != nil {
		return nil, false, err
	}
	defer func() { retErr = errors.Join(retErr, stmt.Close()) }()

	if err := stmt.BindText(1, indexName); err != nil {
		return nil, false, fmt.Errorf("sqlite: binding index name: %w", err)
	}

	columns = make([]string, 0)
	valid = true

	for stmt.Step() {
		cid := stmt.ColumnInt64(1)

		name := stmt.ColumnText(2)
		if cid < 0 || name == "" {
			valid = false
			continue
		}

		columns = append(columns, name)
	}

	if err := stmt.Err(); err != nil {
		return nil, false, fmt.Errorf("sqlite: reading index-column catalog: %w", err)
	}

	if !valid || len(columns) == 0 {
		return nil, false, nil
	}

	return columns, true, nil
}

//nolint:gocyclo // Foreign-key catalog rows form an ordered state machine that validates each transition.
func listSQLiteForeignConstraints(conn rawConnection, table string) (constraints []execution.Constraint, retErr error) {
	constraints = make([]execution.Constraint, 0)

	stmt, err := prepareSQLiteCatalog(conn, listForeignKeysSQL, "foreign-key catalog")
	if err != nil {
		return constraints, err
	}

	defer func() { retErr = errors.Join(retErr, stmt.Close()) }()

	if err := stmt.BindText(1, table); err != nil {
		return constraints, fmt.Errorf("sqlite: binding foreign-key relation: %w", err)
	}

	var (
		current     *execution.Constraint
		currentID   int64
		previousSeq int64
	)

	for stmt.Step() {
		id := stmt.ColumnInt64(0)

		seq := stmt.ColumnInt64(1)
		if id < 0 || seq < 0 {
			return constraints, fmt.Errorf("%w: invalid foreign-key catalog row", execution.ErrInternal)
		}

		if current == nil || id != currentID {
			if current != nil {
				constraints = append(constraints, *current)
			}

			currentID = id
			previousSeq = -1
			current = &execution.Constraint{
				Kind:    "foreign_key",
				Columns: make([]string, 0),
				Referenced: &execution.ConstraintReference{
					Schema:  sqliteMainSchema,
					Table:   stmt.ColumnText(2),
					Columns: make([]string, 0),
				},
			}
		}

		if seq <= previousSeq {
			return constraints, fmt.Errorf("%w: foreign-key sequence is not ordered", execution.ErrInternal)
		}

		previousSeq = seq

		localColumn := stmt.ColumnText(3)
		if localColumn == "" || current.Referenced == nil || current.Referenced.Table == "" {
			return constraints, fmt.Errorf("%w: invalid foreign-key catalog row", execution.ErrInternal)
		}

		current.Columns = append(current.Columns, localColumn)

		if stmt.ColumnType(4) != sqlite3.NULL {
			parentColumn := stmt.ColumnText(4)
			if parentColumn == "" {
				return constraints, fmt.Errorf("%w: invalid foreign-key referenced column", execution.ErrInternal)
			}

			current.Referenced.Columns = append(current.Referenced.Columns, parentColumn)
		}

		if len(current.Columns) == 1 {
			match, err := foreignKeyMatch(stmt.ColumnText(7))
			if err != nil {
				return constraints, err
			}

			update, err := foreignKeyAction(stmt.ColumnText(5))
			if err != nil {
				return constraints, err
			}

			deleteAction, err := foreignKeyAction(stmt.ColumnText(6))
			if err != nil {
				return constraints, err
			}

			current.MatchType = match
			current.UpdateAction = update
			current.DeleteAction = deleteAction
		}
	}

	if err := stmt.Err(); err != nil {
		return constraints, fmt.Errorf("sqlite: reading foreign-key catalog: %w", err)
	}

	if current != nil {
		constraints = append(constraints, *current)
	}

	return constraints, nil
}

func foreignKeyMatch(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", foreignKeyMatchSimple:
		return foreignKeyMatchSimple, nil
	case foreignKeyMatchPartial:
		return foreignKeyMatchPartial, nil
	case foreignKeyMatchFull:
		return foreignKeyMatchFull, nil
	default:
		return "", fmt.Errorf("%w: unknown sqlite foreign-key match %q", execution.ErrInternal, value)
	}
}

func foreignKeyAction(value string) (string, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), " ", "_")) {
	case "no_action":
		return "no_action", nil
	case "restrict":
		return "restrict", nil
	case foreignKeyActionCascade:
		return foreignKeyActionCascade, nil
	case "set_null":
		return "set_null", nil
	case "set_default":
		return "set_default", nil
	default:
		return "", fmt.Errorf("%w: unknown sqlite foreign-key action %q", execution.ErrInternal, value)
	}
}

func prepareSQLiteCatalog(conn rawConnection, sql, label string) (statement, error) {
	stmt, tail, err := conn.Prepare(sql)
	if err != nil {
		return nil, fmt.Errorf("sqlite: preparing %s: %w", label, err)
	}

	if stmt == nil || strings.TrimSpace(tail) != "" {
		invalidErr := fmt.Errorf("%w: invalid %s statement", execution.ErrInternal, label)
		if stmt != nil {
			invalidErr = errors.Join(invalidErr, stmt.Close())
		}

		return nil, invalidErr
	}

	return stmt, nil
}
