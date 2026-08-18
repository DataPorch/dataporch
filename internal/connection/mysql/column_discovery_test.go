package mysql

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/execution"
)

func TestResolveRelationUsesDatabaseAndTableArguments(t *testing.T) {
	t.Parallel()

	rows := &testCatalogRows{values: [][]any{{"BASE TABLE"}}}
	pool := &testCatalogPool{rows: rows}
	queryCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	kind, err := resolveRelation(t.Context(), queryCtx, pool, "finance", "orders")
	if err != nil {
		t.Fatalf("resolveRelation() error = %v", err)
	}
	if kind != execution.RelationKindTable {
		t.Fatalf("relation kind = %q, want %q", kind, execution.RelationKindTable)
	}

	pool.mu.Lock()
	arguments := append([]any(nil), pool.arguments[0]...)
	query := pool.queries[0]
	pool.mu.Unlock()
	if !reflect.DeepEqual(arguments, []any{"finance", "orders"}) {
		t.Fatalf("relation arguments = %#v", arguments)
	}
	if !strings.Contains(query, "FROM INFORMATION_SCHEMA.TABLES") || !strings.Contains(query, "TABLE_NAME = ?") {
		t.Fatalf("relation query = %s", query)
	}
}

func TestResolveRelationReturnsNotFoundForEmptyResult(t *testing.T) {
	t.Parallel()

	pool := &testCatalogPool{rows: &testCatalogRows{}}
	queryCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	_, err := resolveRelation(t.Context(), queryCtx, pool, "finance", "missing")
	if !errors.Is(err, execution.ErrRelationNotFound) {
		t.Fatalf("resolveRelation() error = %v, want relation not found", err)
	}
}

func TestScanColumnMapsMySQLTypesWithoutAffinity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		columnType string
		dataType   string
		category   execution.TypeCategory
		length     *int64
	}{
		{name: "int", columnType: "INT", dataType: "int", category: execution.TypeCategoryBase},
		{name: "unsigned bigint", columnType: "BIGINT UNSIGNED", dataType: "bigint", category: execution.TypeCategoryBase},
		{name: "decimal", columnType: "DECIMAL(12,2)", dataType: "decimal", category: execution.TypeCategoryBase},
		{name: "double", columnType: "DOUBLE", dataType: "double", category: execution.TypeCategoryBase},
		{name: "varchar", columnType: "VARCHAR(255)", dataType: "varchar", category: execution.TypeCategoryBase, length: int64Pointer(255)},
		{name: "longtext", columnType: "LONGTEXT", dataType: "longtext", category: execution.TypeCategoryBase},
		{name: "varbinary", columnType: "VARBINARY(32)", dataType: "varbinary", category: execution.TypeCategoryBase},
		{name: "blob", columnType: "BLOB", dataType: "blob", category: execution.TypeCategoryBase},
		{name: "json", columnType: "JSON", dataType: "json", category: execution.TypeCategoryBase},
		{name: "enum", columnType: "ENUM('new','paid')", dataType: "enum", category: execution.TypeCategoryEnum},
		{name: "set", columnType: "SET('a','b')", dataType: "set", category: execution.TypeCategoryOther},
		{name: "date", columnType: "DATE", dataType: "date", category: execution.TypeCategoryBase},
		{name: "datetime", columnType: "DATETIME(6)", dataType: "datetime", category: execution.TypeCategoryBase},
		{name: "timestamp", columnType: "TIMESTAMP", dataType: "timestamp", category: execution.TypeCategoryBase},
		{name: "time", columnType: "TIME", dataType: "time", category: execution.TypeCategoryBase},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := &testCatalogRows{values: [][]any{{
				"value", index + 1, test.columnType, test.dataType,
				int64Value(test.length), nil, nil, nil, "YES", nil, "", nil, "comment",
			}}}
			row.Next()

			column, err := scanColumn(row, true)
			if err != nil {
				t.Fatalf("scanColumn() error = %v", err)
			}
			if column.FormattedType != test.columnType || column.Type.Name != test.dataType {
				t.Fatalf("column type = %#v, want formatted=%q name=%q", column, test.columnType, test.dataType)
			}
			if column.Type.Category != test.category {
				t.Fatalf("category = %q, want %q", column.Type.Category, test.category)
			}
			if column.Type.Affinity != "" {
				t.Fatalf("affinity = %q, want empty", column.Type.Affinity)
			}
			if test.length == nil && column.Type.Length != nil {
				t.Fatalf("length = %v, want nil", column.Type.Length)
			}
			if test.length != nil && (column.Type.Length == nil || *column.Type.Length != int32(*test.length)) {
				t.Fatalf("length = %v, want %d", column.Type.Length, *test.length)
			}
		})
	}
}

func TestScanColumnMapsNullableMetadataIdentityGeneratedAndDescription(t *testing.T) {
	t.Parallel()

	row := &testCatalogRows{values: [][]any{{
		"created_at", 1, "DATETIME(6)", "datetime", nil, nil, nil, int64(6),
		"NO", "CURRENT_TIMESTAMP", "STORED GENERATED AUTO_INCREMENT", "created_at + interval 1 day", "created",
	}}}
	row.Next()

	column, err := scanColumn(row, true)
	if err != nil {
		t.Fatalf("scanColumn() error = %v", err)
	}
	if column.Nullable {
		t.Fatal("Nullable = true, want false")
	}
	if column.Type.TemporalPrecision == nil || *column.Type.TemporalPrecision != 6 {
		t.Fatalf("temporal precision = %v, want 6", column.Type.TemporalPrecision)
	}
	if column.Identity == nil || column.Identity.Generation != "by_default" {
		t.Fatalf("identity = %#v, want by_default", column.Identity)
	}
	if column.Generated == nil || column.Generated.Kind != "stored" || column.Generated.Expression != "created_at + interval 1 day" {
		t.Fatalf("generated = %#v", column.Generated)
	}
	if column.DefaultExpression != nil {
		t.Fatalf("DefaultExpression = %v, want nil for generated column", column.DefaultExpression)
	}
	if column.Description == nil || *column.Description != "created" {
		t.Fatalf("Description = %v, want created", column.Description)
	}

	row = &testCatalogRows{values: [][]any{{
		"name", 2, "VARCHAR(255)", "varchar", int64(255), nil, nil, nil,
		"YES", "guest", "", nil, "hidden",
	}}}
	row.Next()
	column, err = scanColumn(row, false)
	if err != nil {
		t.Fatalf("scanColumn(includeDescriptions=false) error = %v", err)
	}
	if column.Description != nil {
		t.Fatalf("Description = %v, want nil", column.Description)
	}
}

func TestScanColumnDropsOutOfRangeNumericMetadata(t *testing.T) {
	t.Parallel()

	tooLarge := int64(math.MaxInt32) + 1
	negative := int64(-1)
	row := &testCatalogRows{values: [][]any{{
		"amount", 1, "DECIMAL", "decimal", tooLarge, tooLarge, negative, tooLarge,
		"YES", nil, "", nil, nil,
	}}}
	row.Next()

	column, err := scanColumn(row, false)
	if err != nil {
		t.Fatalf("scanColumn() error = %v", err)
	}
	if column.Type.Length != nil || column.Type.Precision != nil || column.Type.Scale != nil || column.Type.TemporalPrecision != nil {
		t.Fatalf("out-of-range metadata = %#v, want all nil", column.Type)
	}
}

func TestListColumnsResolvesRelationAndUsesParameterizedColumnQuery(t *testing.T) {
	t.Parallel()

	search := `%_literal`
	pool := &testCatalogPool{results: []testCatalogResult{
		{rows: &testCatalogRows{values: [][]any{{"VIEW"}}}},
		{rows: &testCatalogRows{values: [][]any{
			{"id", 1, "INT", "int", nil, nil, nil, nil, "NO", nil, "AUTO_INCREMENT", nil, "id"},
			{"name", 2, "VARCHAR(255)", "varchar", int64(255), nil, nil, nil, "YES", nil, "", nil, "name"},
		}}},
	}}
	opener := &testClientOpener{client: &Client{pool: pool, database: "finance"}}
	discoverer := lifecycleTestDiscoverer(t, opener)

	page, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID:            "source-1",
		Schema:              "finance",
		Table:               "orders",
		Search:              search,
		IncludeDescriptions: true,
		Limit:               1,
		AfterOrdinal:        3,
	})
	if err != nil {
		t.Fatalf("ListColumns() error = %v", err)
	}
	if len(page.Columns) != 1 || !page.HasMore || page.RelationKind != execution.RelationKindView {
		t.Fatalf("page = %#v, want one view column with more", page)
	}
	if page.Columns[0].Identity == nil || page.Columns[0].Identity.Generation != "by_default" {
		t.Fatalf("identity = %#v", page.Columns[0].Identity)
	}

	pool.mu.Lock()
	arguments := make([][]any, len(pool.arguments))
	for index := range pool.arguments {
		arguments[index] = append([]any(nil), pool.arguments[index]...)
	}
	queries := append([]string(nil), pool.queries...)
	pool.mu.Unlock()
	if !reflect.DeepEqual(arguments[0], []any{"finance", "orders"}) {
		t.Fatalf("relation arguments = %#v", arguments[0])
	}
	wantColumnArguments := []any{true, "finance", "orders", search, search, 3, 2}
	if !reflect.DeepEqual(arguments[1], wantColumnArguments) {
		t.Fatalf("column arguments = %#v, want %#v", arguments[1], wantColumnArguments)
	}
	if strings.Contains(queries[1], search) {
		t.Fatalf("column query contains user search %q: %s", search, queries[1])
	}
}

func TestListColumnsRejectsOutsideImportedSchemaBeforeQueries(t *testing.T) {
	t.Parallel()

	pool := &testCatalogPool{}
	opener := &testClientOpener{client: &Client{pool: pool, database: "finance"}}
	discoverer := lifecycleTestDiscoverer(t, opener)

	_, err := discoverer.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID: "finance",
		Schema:   "other",
		Table:    "orders",
		Limit:    1,
	})
	if !errors.Is(err, execution.ErrSchemaNotFound) {
		t.Fatalf("ListColumns() error = %v, want schema not found", err)
	}
	pool.mu.Lock()
	queryCount := pool.queryCount
	pool.mu.Unlock()
	if queryCount != 0 {
		t.Fatalf("query count = %d, want 0", queryCount)
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}

func int64Value(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
