package mysql

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/execution"
)

//nolint:gocyclo // The fixture asserts each constraint family and cross-database redaction rule.
func TestListConstraintsAggregatesAndRedactsReferences(t *testing.T) {
	t.Parallel()

	rows := &testCatalogRows{values: [][]any{
		{"orders_pkey", "PRIMARY KEY", "id", int64(1), nil, nil, nil, nil, nil, nil, nil, "YES"},
		{"orders_unique", "UNIQUE", "tenant_id", int64(1), nil, nil, nil, nil, nil, nil, nil, "YES"},
		{"orders_unique", "UNIQUE", "external_id", int64(2), nil, nil, nil, nil, nil, nil, nil, "YES"},
		{"orders_customer_fk", "FOREIGN KEY", "customer_id", int64(1), "finance", "customers", "id", "NONE", "CASCADE", "SET NULL", nil, "YES"},
		{"orders_external_fk", "FOREIGN KEY", "external_id", int64(1), "other_database", "customers", "id", "FULL", "RESTRICT", "NO ACTION", nil, "YES"},
		{"orders_amount_check", "CHECK", nil, nil, nil, nil, nil, nil, nil, nil, "amount >= 0", "YES"},
		{"orders_status_check", "CHECK", nil, nil, nil, nil, nil, nil, nil, nil, "status <> ''", "NO"},
		{"orders_hidden_unique", "UNIQUE", "not_returned", int64(1), nil, nil, nil, nil, nil, nil, nil, "YES"},
	}}
	pool := &testCatalogPool{rows: rows}

	queryCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	columns := []execution.Column{
		{Name: "id"},
		{Name: "tenant_id"},
		{Name: "external_id"},
		{Name: "customer_id"},
		{Name: "amount"},
	}

	constraints, err := listConstraints(t.Context(), queryCtx, pool, "finance", "orders", columns)
	if err != nil {
		t.Fatalf("listConstraints() error = %v", err)
	}

	if len(constraints) != 6 {
		t.Fatalf("constraints = %#v, want hidden non-overlapping constraint omitted", constraints)
	}

	if constraints[0].Kind != "primary_key" || !constraints[0].Validated || !reflect.DeepEqual(constraints[0].Columns, []string{"id"}) {
		t.Fatalf("primary key = %#v", constraints[0])
	}

	if constraints[1].Kind != "unique" || !reflect.DeepEqual(constraints[1].Columns, []string{"tenant_id", "external_id"}) {
		t.Fatalf("composite unique = %#v", constraints[1])
	}

	foreign := constraints[2]
	if foreign.Kind != "foreign_key" || foreign.Referenced == nil ||
		foreign.Referenced.Schema != "finance" || foreign.Referenced.Table != "customers" ||
		!reflect.DeepEqual(foreign.Referenced.Columns, []string{"id"}) ||
		foreign.MatchType != "" || foreign.UpdateAction != "cascade" || foreign.DeleteAction != "set_null" {
		t.Fatalf("same-database foreign key = %#v", foreign)
	}

	crossDatabase := constraints[3]
	if crossDatabase.Kind != "foreign_key" || crossDatabase.Referenced != nil ||
		!reflect.DeepEqual(crossDatabase.Columns, []string{"external_id"}) ||
		crossDatabase.UpdateAction != "restrict" || crossDatabase.DeleteAction != "no_action" ||
		crossDatabase.MatchType != "full" {
		t.Fatalf("cross-database foreign key = %#v", crossDatabase)
	}

	check := constraints[4]
	if check.Kind != "check" || check.Columns == nil || len(check.Columns) != 0 ||
		check.CheckExpression == nil || *check.CheckExpression != "amount >= 0" || !check.Validated {
		t.Fatalf("enforced check = %#v", check)
	}

	notEnforced := constraints[5]
	if notEnforced.Kind != "check" || notEnforced.Validated || notEnforced.CheckExpression == nil {
		t.Fatalf("not-enforced check = %#v", notEnforced)
	}

	pool.mu.Lock()
	arguments := append([]any(nil), pool.arguments[0]...)
	query := pool.queries[0]
	pool.mu.Unlock()

	if !reflect.DeepEqual(arguments, []any{"finance", "finance", "orders"}) {
		t.Fatalf("constraint query arguments = %#v", arguments)
	}

	if !strings.Contains(query, "INFORMATION_SCHEMA.TABLE_CONSTRAINTS") || !strings.Contains(query, "TABLE_SCHEMA = ?") {
		t.Fatalf("constraint query = %s", query)
	}
}

func TestListConstraintsRejectsUnknownConstraintType(t *testing.T) {
	t.Parallel()

	_, err := constraintKind("INDEX")
	if !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("constraintKind() error = %v, want internal error", err)
	}
}

func TestListColumnsAttachesConstraintsOnlyOnFirstPage(t *testing.T) {
	t.Parallel()

	newPool := func() *testCatalogPool {
		return &testCatalogPool{results: []testCatalogResult{
			{rows: &testCatalogRows{values: [][]any{{"BASE TABLE"}}}},
			{rows: &testCatalogRows{values: [][]any{{
				"id", 1, "INT", "int", nil, nil, nil, nil, "NO", nil, "", nil, nil,
			}}}},
			{rows: &testCatalogRows{values: [][]any{{
				"orders_pkey", "PRIMARY KEY", "id", int64(1), nil, nil, nil, nil, nil, nil, nil, "YES",
			}}}},
		}}
	}

	t.Run("first page", func(t *testing.T) {
		t.Parallel()

		pool := newPool()
		discoverer := lifecycleTestDiscoverer(t, &testClientOpener{client: &Client{pool: pool, database: "finance"}})

		page, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
			SourceID: "finance",
			Schema:   "finance",
			Table:    "orders",
			Limit:    1,
		})
		if err != nil {
			t.Fatalf("ListColumns() error = %v", err)
		}

		if len(page.Constraints) != 1 || page.Constraints[0].Kind != "primary_key" {
			t.Fatalf("constraints = %#v", page.Constraints)
		}

		pool.mu.Lock()
		queryCount := pool.queryCount
		pool.mu.Unlock()

		if queryCount != 3 {
			t.Fatalf("query count = %d, want relation, columns, constraints", queryCount)
		}
	})

	t.Run("later page", func(t *testing.T) {
		t.Parallel()

		pool := newPool()
		discoverer := lifecycleTestDiscoverer(t, &testClientOpener{client: &Client{pool: pool, database: "finance"}})

		page, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
			SourceID:     "finance",
			Schema:       "finance",
			Table:        "orders",
			Limit:        1,
			AfterOrdinal: 1,
		})
		if err != nil {
			t.Fatalf("ListColumns() error = %v", err)
		}

		if page.Constraints == nil || len(page.Constraints) != 0 {
			t.Fatalf("constraints = %#v, want empty later-page slice", page.Constraints)
		}

		pool.mu.Lock()
		queryCount := pool.queryCount
		pool.mu.Unlock()

		if queryCount != 2 {
			t.Fatalf("query count = %d, want relation and columns only", queryCount)
		}
	})
}
