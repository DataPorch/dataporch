package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/execution"
)

func TestListConstraintsSQLSupportsPostgreSQL14(t *testing.T) {
	t.Parallel()

	if strings.Contains(listConstraintsSQL, "constraint_index.indnullsnotdistinct") {
		t.Fatal("listConstraintsSQL directly references PostgreSQL 15-only indnullsnotdistinct")
	}

	if !strings.Contains(
		listConstraintsSQL,
		"pg_catalog.to_jsonb(constraint_index) ->> 'indnullsnotdistinct'",
	) {
		t.Fatal("listConstraintsSQL does not use version-neutral index metadata lookup")
	}
}

//nolint:gocyclo // The fixture covers complete composite and foreign-key constraint mappings.
func TestListConstraintsMapsCompleteCompositeAndForeignKeys(t *testing.T) {
	t.Parallel()

	pool := &testCatalogPool{results: []testCatalogResult{{rows: &testCatalogRows{values: [][]any{
		{
			"accounts_pkey", "p", true, false,
			[]string{"tenant_id", "account_id"},
			nil, nil,
			[]string{},
			"", "", "", nil, nil, true,
		},
		{
			"accounts_owner_fk", "f", false, true,
			[]string{"tenant_id", "owner_id"},
			"security", "users",
			[]string{"tenant_id", "id"},
			"s", "c", "n", nil, nil, true,
		},
		{
			"accounts_code_check", "c", false, false,
			[]string{"code"},
			nil, nil,
			[]string{},
			"", "", "", nil, "code <> ''", false,
		},
	}}}}}

	discoverer, err := newDiscoverer(&testClientOpener{client: &Client{pool: pool}}, time.Second)
	if err != nil {
		t.Fatalf("newDiscoverer() error = %v", err)
	}

	queryCtx, cancel := discoverer.queryContext(t.Context())
	defer cancel()

	constraints, err := listConstraints(
		t.Context(),
		queryCtx,
		pool,
		42,
		[]int16{1, 2},
	)
	if err != nil {
		t.Fatalf("listConstraints() error = %v", err)
	}

	if len(constraints) != 3 || constraints[0].Kind != "primary_key" || constraints[0].Columns[1] != "account_id" {
		t.Fatalf("constraints = %#v", constraints)
	}

	foreign := constraints[1]

	foreignIsComplete := foreign.Kind == "foreign_key" &&
		foreign.Referenced != nil &&
		foreign.Referenced.Schema == "security" &&
		foreign.Referenced.Columns[1] == "id" &&
		foreign.MatchType == "simple" &&
		foreign.UpdateAction == "cascade" &&
		foreign.DeleteAction == "set_null"
	if !foreignIsComplete {
		t.Fatalf("foreign constraint = %#v", foreign)
	}

	check := constraints[2]
	if check.CheckExpression == nil ||
		*check.CheckExpression != "code <> ''" ||
		check.Validated {
		t.Fatalf("check constraint = %#v", constraints[2])
	}

	if pool.allArguments[0][0] != uint32(42) {
		t.Fatalf("relation OID argument = %#v", pool.allArguments[0][0])
	}

	if got, ok := pool.allArguments[0][1].([]int16); !ok || len(got) != 2 || got[1] != 2 {
		t.Fatalf("attnum argument = %#v", pool.allArguments[0][1])
	}
}

func TestListConstraintsSkipsEmptyPage(t *testing.T) {
	t.Parallel()

	pool := &testCatalogPool{}

	queryCtx, cancel := contextWithCancel(t)
	defer cancel()

	constraints, err := listConstraints(
		t.Context(),
		queryCtx,
		pool,
		42,
		nil,
	)
	if err != nil {
		t.Fatalf("listConstraints() error = %v", err)
	}

	if constraints == nil || len(constraints) != 0 || pool.queryCount != 0 {
		t.Fatalf("empty constraints = %#v/query count %d", constraints, pool.queryCount)
	}
}

func TestConstraintMappingsRejectUnknownCodes(t *testing.T) {
	t.Parallel()

	if _, err := constraintKind("x"); !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("constraintKind(x) error = %v, want internal", err)
	}

	if _, _, _, err := foreignKeyActions("x", "a", "a"); !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("foreignKeyActions(match) error = %v, want internal", err)
	}

	if _, _, _, err := foreignKeyActions("s", "x", "a"); !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("foreignKeyActions(update) error = %v, want internal", err)
	}

	if _, _, _, err := foreignKeyActions("s", "a", "x"); !errors.Is(err, execution.ErrInternal) {
		t.Fatalf("foreignKeyActions(delete) error = %v, want internal", err)
	}
}

func contextWithCancel(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithCancel(t.Context())
}
