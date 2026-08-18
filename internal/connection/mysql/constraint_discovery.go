package mysql

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/adamraziv/dataporch/internal/execution"
)

const listConstraintsSQL = `
SELECT
    tc.CONSTRAINT_NAME,
    tc.CONSTRAINT_TYPE,
    kcu.COLUMN_NAME,
    kcu.ORDINAL_POSITION,
    kcu.REFERENCED_TABLE_SCHEMA,
    kcu.REFERENCED_TABLE_NAME,
    kcu.REFERENCED_COLUMN_NAME,
    rc.MATCH_OPTION,
    rc.UPDATE_RULE,
    rc.DELETE_RULE,
    cc.CHECK_CLAUSE,
    tc.ENFORCED
FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS AS tc
LEFT JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE AS kcu
  ON kcu.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
 AND kcu.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
 AND kcu.TABLE_SCHEMA = tc.TABLE_SCHEMA
 AND kcu.TABLE_NAME = tc.TABLE_NAME
LEFT JOIN INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS AS rc
  ON rc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
 AND rc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
 AND rc.TABLE_NAME = tc.TABLE_NAME
LEFT JOIN INFORMATION_SCHEMA.CHECK_CONSTRAINTS AS cc
  ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
 AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
WHERE tc.CONSTRAINT_SCHEMA = ?
  AND tc.TABLE_SCHEMA = ?
  AND tc.TABLE_NAME = ?
  AND tc.CONSTRAINT_TYPE IN ('PRIMARY KEY', 'UNIQUE', 'FOREIGN KEY', 'CHECK')
ORDER BY CAST(tc.CONSTRAINT_NAME AS BINARY), kcu.ORDINAL_POSITION`

type mysqlConstraintRow struct {
	name             string
	constraintType   string
	columnName       *string
	ordinal          *int64
	referencedSchema *string
	referencedTable  *string
	referencedColumn *string
	matchOption      *string
	updateRule       *string
	deleteRule       *string
	checkClause      *string
	enforced         bool
}

type mysqlConstraintAccumulator struct {
	name              string
	constraintType    string
	columns           []orderedConstraintColumn
	referencedSchema  *string
	referencedTable   *string
	referencedColumns []orderedConstraintColumn
	matchOption       string
	updateRule        string
	deleteRule        string
	checkClause       *string
	enforced          bool
}

type orderedConstraintColumn struct {
	ordinal int64
	name    string
}

func listConstraints(
	parentCtx context.Context,
	queryCtx context.Context,
	pool runtimePool,
	database string,
	table string,
	columns []execution.Column,
) (constraints []execution.Constraint, retErr error) {
	constraints = make([]execution.Constraint, 0)
	rows, err := pool.Query(queryCtx, listConstraintsSQL, database, database, table)
	if err != nil {
		return nil, classifyDiscoveryQueryError(parentCtx, queryCtx, err)
	}
	if isNilInterface(rows) {
		return nil, fmt.Errorf("%w: nil constraint rows", execution.ErrInternal)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			retErr = errors.Join(
				retErr,
				fmt.Errorf("%w: closing constraint rows: %w", execution.ErrInternal, closeErr),
			)
		}
	}()

	byName := make(map[string]*mysqlConstraintAccumulator)
	ordered := make([]*mysqlConstraintAccumulator, 0)
	for rows.Next() {
		row, err := scanConstraintRow(rows)
		if err != nil {
			return nil, classifyDiscoveryQueryError(parentCtx, queryCtx, err)
		}

		constraint := byName[row.name]
		if constraint == nil {
			constraint = &mysqlConstraintAccumulator{
				name:           row.name,
				constraintType: row.constraintType,
				enforced:       row.enforced,
			}
			byName[row.name] = constraint
			ordered = append(ordered, constraint)
		}
		if row.columnName != nil && row.constraintType != "CHECK" {
			constraint.columns = append(constraint.columns, orderedConstraintColumn{
				ordinal: int64ValueOrZero(row.ordinal),
				name:    strings.Clone(*row.columnName),
			})
		}
		if row.referencedSchema != nil {
			constraint.referencedSchema = stringPointerClone(row.referencedSchema)
		}
		if row.referencedTable != nil {
			constraint.referencedTable = stringPointerClone(row.referencedTable)
		}
		if row.referencedColumn != nil {
			constraint.referencedColumns = append(constraint.referencedColumns, orderedConstraintColumn{
				ordinal: int64ValueOrZero(row.ordinal),
				name:    strings.Clone(*row.referencedColumn),
			})
		}
		if row.matchOption != nil {
			constraint.matchOption = strings.Clone(*row.matchOption)
		}
		if row.updateRule != nil {
			constraint.updateRule = strings.Clone(*row.updateRule)
		}
		if row.deleteRule != nil {
			constraint.deleteRule = strings.Clone(*row.deleteRule)
		}
		if row.checkClause != nil {
			constraint.checkClause = stringPointerClone(row.checkClause)
		}
		constraint.enforced = row.enforced
	}

	if err := rows.Err(); err != nil {
		return nil, classifyDiscoveryQueryError(parentCtx, queryCtx, err)
	}

	retainedColumns := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		retainedColumns[column.Name] = struct{}{}
	}
	for _, accumulator := range ordered {
		constraint, err := materializeConstraint(accumulator, database, retainedColumns)
		if err != nil {
			return nil, err
		}
		if constraint == nil {
			continue
		}
		constraints = append(constraints, *constraint)
	}

	return constraints, nil
}

func scanConstraintRow(rows catalogRows) (mysqlConstraintRow, error) {
	var (
		row              mysqlConstraintRow
		columnName       *string
		ordinal          *int64
		referencedSchema *string
		referencedTable  *string
		referencedColumn *string
		matchOption      *string
		updateRule       *string
		deleteRule       *string
		checkClause      *string
		enforcedText     string
	)
	if err := rows.Scan(
		&row.name,
		&row.constraintType,
		&columnName,
		&ordinal,
		&referencedSchema,
		&referencedTable,
		&referencedColumn,
		&matchOption,
		&updateRule,
		&deleteRule,
		&checkClause,
		&enforcedText,
	); err != nil {
		return mysqlConstraintRow{}, err
	}

	row.columnName = columnName
	row.ordinal = ordinal
	row.referencedSchema = referencedSchema
	row.referencedTable = referencedTable
	row.referencedColumn = referencedColumn
	row.matchOption = matchOption
	row.updateRule = updateRule
	row.deleteRule = deleteRule
	row.checkClause = checkClause
	row.enforced = strings.EqualFold(strings.TrimSpace(enforcedText), "YES")
	return row, nil
}

func materializeConstraint(
	accumulator *mysqlConstraintAccumulator,
	database string,
	retainedColumns map[string]struct{},
) (*execution.Constraint, error) {
	kind, err := constraintKind(accumulator.constraintType)
	if err != nil {
		return nil, err
	}

	columns := orderedColumnNames(accumulator.columns)
	if kind != "check" && !hasRetainedColumn(columns, retainedColumns) {
		return nil, nil
	}
	if kind == "check" {
		columns = make([]string, 0)
	}

	constraint := &execution.Constraint{
		Name:      accumulator.name,
		Kind:      kind,
		Columns:   columns,
		Validated: accumulator.enforced,
	}
	if accumulator.checkClause != nil && kind == "check" {
		constraint.CheckExpression = stringPointerClone(accumulator.checkClause)
	}

	if kind == "foreign_key" {
		constraint.MatchType, err = normalizeMatchOption(accumulator.matchOption)
		if err != nil {
			return nil, err
		}
		constraint.UpdateAction, err = normalizeForeignKeyAction(accumulator.updateRule)
		if err != nil {
			return nil, err
		}
		constraint.DeleteAction, err = normalizeForeignKeyAction(accumulator.deleteRule)
		if err != nil {
			return nil, err
		}
		if accumulator.referencedSchema != nil && accumulator.referencedTable != nil &&
			*accumulator.referencedSchema == database {
			constraint.Referenced = &execution.ConstraintReference{
				Schema:  strings.Clone(*accumulator.referencedSchema),
				Table:   strings.Clone(*accumulator.referencedTable),
				Columns: orderedColumnNames(accumulator.referencedColumns),
			}
		}
	}

	return constraint, nil
}

func constraintKind(value string) (string, error) {
	switch value {
	case "PRIMARY KEY":
		return "primary_key", nil
	case "UNIQUE":
		return "unique", nil
	case "FOREIGN KEY":
		return "foreign_key", nil
	case "CHECK":
		return "check", nil
	default:
		return "", fmt.Errorf("%w: unknown mysql constraint type", execution.ErrInternal)
	}
}

func normalizeMatchOption(value string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "NONE":
		return "", nil
	case "FULL":
		return "full", nil
	case "PARTIAL":
		return "partial", nil
	default:
		return "", fmt.Errorf("%w: unknown mysql foreign key match option", execution.ErrInternal)
	}
}

func normalizeForeignKeyAction(value string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "CASCADE":
		return "cascade", nil
	case "SET NULL":
		return "set_null", nil
	case "SET DEFAULT":
		return "set_default", nil
	case "RESTRICT":
		return "restrict", nil
	case "NO ACTION":
		return "no_action", nil
	default:
		return "", fmt.Errorf("%w: unknown mysql foreign key action", execution.ErrInternal)
	}
}

func orderedColumnNames(values []orderedConstraintColumn) []string {
	ordered := append([]orderedConstraintColumn(nil), values...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].ordinal < ordered[j].ordinal
	})

	names := make([]string, 0, len(ordered))
	for _, value := range ordered {
		names = append(names, strings.Clone(value.name))
	}
	return names
}

func hasRetainedColumn(columns []string, retained map[string]struct{}) bool {
	for _, column := range columns {
		if _, ok := retained[column]; ok {
			return true
		}
	}
	return false
}

func int64ValueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func stringPointerClone(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := strings.Clone(*value)
	return &cloned
}
