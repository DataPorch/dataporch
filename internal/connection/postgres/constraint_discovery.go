package postgres

import (
	"context"
	"fmt"

	"github.com/adamraziv/dataporch/internal/execution"
)

const listConstraintsSQL = `
SELECT
    con.conname,
    con.contype::text,
    con.condeferrable,
    con.condeferred,
    ARRAY(
      SELECT a.attname
      FROM unnest(con.conkey) WITH ORDINALITY AS key(attnum, ordinal)
      JOIN pg_catalog.pg_attribute AS a
        ON a.attrelid = con.conrelid AND a.attnum = key.attnum
      ORDER BY key.ordinal
    ),
    referenced_ns.nspname,
    referenced_class.relname,
    ARRAY(
      SELECT a.attname
      FROM unnest(con.confkey) WITH ORDINALITY AS key(attnum, ordinal)
      JOIN pg_catalog.pg_attribute AS a
        ON a.attrelid = con.confrelid AND a.attnum = key.attnum
      ORDER BY key.ordinal
    ),
    con.confmatchtype::text,
    con.confupdtype::text,
    con.confdeltype::text,
    CASE WHEN con.contype = 'u'
      THEN COALESCE(
        (pg_catalog.to_jsonb(constraint_index) ->> 'indnullsnotdistinct')::boolean,
        false
      )
    END,
    CASE WHEN con.contype = 'c'
      THEN pg_catalog.pg_get_expr(con.conbin, con.conrelid, true)
    END,
    con.convalidated
FROM pg_catalog.pg_constraint AS con
LEFT JOIN pg_catalog.pg_index AS constraint_index ON constraint_index.indexrelid = con.conindid
LEFT JOIN pg_catalog.pg_class AS referenced_class ON referenced_class.oid = con.confrelid
LEFT JOIN pg_catalog.pg_namespace AS referenced_ns
  ON referenced_ns.oid = referenced_class.relnamespace
WHERE con.conrelid = $1
  AND con.contype IN ('p', 'u', 'f', 'c')
  AND con.conkey && $2::smallint[]
  AND NOT EXISTS (
    SELECT 1
    FROM unnest(con.conkey) AS local_key(attnum)
    WHERE NOT (
      pg_catalog.has_table_privilege(con.conrelid, 'SELECT')
      OR pg_catalog.has_column_privilege(con.conrelid, local_key.attnum, 'SELECT')
    )
  )
  AND (
    con.contype <> 'f'
    OR (
      pg_catalog.has_schema_privilege(referenced_class.relnamespace, 'USAGE')
      AND NOT EXISTS (
        SELECT 1
        FROM unnest(con.confkey) AS referenced_key(attnum)
        WHERE NOT (
          pg_catalog.has_table_privilege(con.confrelid, 'SELECT')
          OR pg_catalog.has_column_privilege(
            con.confrelid,
            referenced_key.attnum,
            'SELECT'
          )
        )
      )
    )
  )
ORDER BY con.conname COLLATE "C", con.oid`

func listConstraints(
	parentCtx context.Context,
	queryCtx context.Context,
	pool runtimePool,
	relationOID uint32,
	attnums []int16,
) ([]execution.Constraint, error) {
	constraints := make([]execution.Constraint, 0)
	if len(attnums) == 0 {
		return constraints, nil
	}

	rows, err := pool.Query(queryCtx, listConstraintsSQL, relationOID, attnums)
	if err != nil {
		return nil, classifyQueryError(parentCtx, queryCtx, err)
	}

	if rows == nil {
		return nil, fmt.Errorf("%w: nil constraint rows", execution.ErrInternal)
	}
	defer rows.Close()

	for rows.Next() {
		constraint, err := scanConstraint(rows)
		if err != nil {
			return nil, classifyQueryError(parentCtx, queryCtx, err)
		}

		constraints = append(constraints, constraint)
	}

	if err := rows.Err(); err != nil {
		return nil, classifyQueryError(parentCtx, queryCtx, err)
	}

	return constraints, nil
}

func scanConstraint(rows catalogRows) (execution.Constraint, error) {
	var (
		name              string
		constraintCode    string
		deferrable        bool
		initiallyDeferred bool
		columns           []string
		referencedSchema  *string
		referencedTable   *string
		referencedColumns []string
		matchCode         string
		updateCode        string
		deleteCode        string
		nullsNotDistinct  *bool
		checkExpression   *string
		validated         bool
	)
	if err := rows.Scan(
		&name,
		&constraintCode,
		&deferrable,
		&initiallyDeferred,
		&columns,
		&referencedSchema,
		&referencedTable,
		&referencedColumns,
		&matchCode,
		&updateCode,
		&deleteCode,
		&nullsNotDistinct,
		&checkExpression,
		&validated,
	); err != nil {
		return execution.Constraint{}, err
	}

	kind, err := constraintKind(constraintCode)
	if err != nil {
		return execution.Constraint{}, err
	}

	constraint := execution.Constraint{
		Name:              name,
		Kind:              kind,
		Columns:           cloneStringsSlice(columns),
		Deferrable:        deferrable,
		InitiallyDeferred: initiallyDeferred,
		NullsNotDistinct:  cloneConstraintBoolPointer(nullsNotDistinct),
		CheckExpression:   cloneConstraintStringPointer(checkExpression),
		Validated:         validated,
	}

	if constraintCode == "f" {
		matchType, updateAction, deleteAction, err := foreignKeyActions(matchCode, updateCode, deleteCode)
		if err != nil {
			return execution.Constraint{}, err
		}

		if referencedSchema == nil || referencedTable == nil {
			return execution.Constraint{}, fmt.Errorf("%w: foreign key reference missing", execution.ErrInternal)
		}

		constraint.Referenced = &execution.ConstraintReference{
			Schema:  *referencedSchema,
			Table:   *referencedTable,
			Columns: cloneStringsSlice(referencedColumns),
		}
		constraint.MatchType = matchType
		constraint.UpdateAction = updateAction
		constraint.DeleteAction = deleteAction
	}

	return constraint, nil
}

func constraintKind(code string) (string, error) {
	switch code {
	case "p":
		return "primary_key", nil
	case "u":
		return "unique", nil
	case "f":
		return "foreign_key", nil
	case "c":
		return "check", nil
	default:
		return "", fmt.Errorf("%w: unknown constraint code", execution.ErrInternal)
	}
}

func foreignKeyActions(matchCode, updateCode, deleteCode string) (string, string, string, error) {
	matchType, ok := map[string]string{"f": "full", "p": "partial", "s": "simple"}[matchCode]
	if !ok {
		return "", "", "", fmt.Errorf("%w: unknown foreign key match code", execution.ErrInternal)
	}

	updateAction, ok := foreignKeyAction(updateCode)
	if !ok {
		return "", "", "", fmt.Errorf("%w: unknown foreign key update code", execution.ErrInternal)
	}

	deleteAction, ok := foreignKeyAction(deleteCode)
	if !ok {
		return "", "", "", fmt.Errorf("%w: unknown foreign key delete code", execution.ErrInternal)
	}

	return matchType, updateAction, deleteAction, nil
}

func foreignKeyAction(code string) (string, bool) {
	actions := map[string]string{
		"a": "no_action",
		"r": "restrict",
		"c": "cascade",
		"n": "set_null",
		"d": "set_default",
	}
	action, ok := actions[code]

	return action, ok
}

func cloneStringsSlice(values []string) []string {
	cloned := make([]string, len(values))
	copy(cloned, values)

	return cloned
}

func cloneConstraintStringPointer(value *string) *string {
	if value == nil {
		return nil
	}

	cloned := *value

	return &cloned
}

func cloneConstraintBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}

	cloned := *value

	return &cloned
}
