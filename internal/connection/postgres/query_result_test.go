package postgres

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
)

func TestEncodedRawRowSizeMatchesJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value [][]byte
	}{
		{name: "empty", value: [][]byte{}},
		{name: "null", value: [][]byte{nil}},
		{name: "quotes", value: [][]byte{[]byte(`quote"slash\\`)}},
		{name: "controls", value: [][]byte{{'\n'}, {'\t'}}},
		{name: "unicode", value: [][]byte{[]byte("你好")}},
		{name: "invalid utf8", value: [][]byte{{0xff, 0xfe}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, fits, err := encodedRawRowSize(test.value, 1<<20)
			if err != nil || !fits {
				t.Fatalf("encodedRawRowSize() = %d, %t, %v", got, fits, err)
			}

			values := make([]any, len(test.value))
			for index, value := range test.value {
				if value != nil {
					values[index] = string(value)
				}
			}

			encoded, err := json.Marshal(values)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}

			if got != len(encoded) {
				t.Fatalf("encoded size = %d, want %d (%s)", got, len(encoded), encoded)
			}
		})
	}
}

func TestEncodedRawRowSizeRejectsBeforeAllocationBudget(t *testing.T) {
	t.Parallel()

	if got, fits, err := encodedRawRowSize([][]byte{[]byte("12345")}, 6); err != nil || fits || got != 0 {
		t.Fatalf("encodedRawRowSize() = %d, %t, %v, want zero and does-not-fit", got, fits, err)
	}
}

func TestQueryResultBudgetMatchesEncodedResult(t *testing.T) {
	t.Parallel()

	values := []string{"quote\"", "line\n", "你好"}
	for _, truncated := range []bool{false, true} {
		for count := 0; count <= len(values); count++ {
			t.Run(budgetCaseName(truncated, count), func(t *testing.T) {
				t.Parallel()

				columns := []execution.RelationalQueryColumn{
					{Name: "value", DatabaseType: "text"},
					{Name: "value", DatabaseType: "text"},
				}
				result := execution.RelationalQueryResult{
					Kind:      "postgres",
					SourceID:  connection.ID("finance"),
					Columns:   columns,
					Rows:      make([][]*string, 0, count),
					Truncated: truncated,
				}

				budget, err := newQueryResultBudget(result, 1<<20)
				if err != nil {
					t.Fatalf("newQueryResultBudget() error = %v", err)
				}

				for index := range count {
					value := values[index]
					row := [][]byte{[]byte(value), nil}

					rowSize, fits, err := encodedRawRowSize(row, 1<<20)
					if err != nil || !fits || !budget.FitsAdditionalRow(rowSize) {
						t.Fatalf("row %d does not fit: size=%d fits=%t err=%v", index, rowSize, fits, err)
					}

					first := value
					result.Rows = append(result.Rows, []*string{&first, nil})

					budget.RetainRow(rowSize)
				}

				result.RowCount = len(result.Rows)

				encoded, err := json.Marshal(result)
				if err != nil {
					t.Fatalf("Marshal() error = %v", err)
				}

				calculated := budget.fixedSize + budget.rowsSize + len(strconv.Itoa(result.RowCount)) + len(strconv.FormatBool(result.Truncated))
				if calculated != len(encoded) {
					t.Fatalf("budget size = %d, encoded size = %d (%s)", calculated, len(encoded), encoded)
				}

				if !budget.fits(calculated) {
					t.Fatal("budget should fit its exact encoded result")
				}
			})
		}
	}
}

func TestQueryResultBudgetRejectsMetadataAndNextRow(t *testing.T) {
	t.Parallel()

	result := execution.RelationalQueryResult{
		Kind:     "postgres",
		SourceID: "finance",
		Columns:  []execution.RelationalQueryColumn{{Name: "long column", DatabaseType: "text"}},
		Rows:     [][]*string{},
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	if _, err := newQueryResultBudget(result, len(encoded)-1); err == nil {
		t.Fatal("newQueryResultBudget() error = nil, want metadata size error")
	}

	budget, err := newQueryResultBudget(result, len(encoded)+1)
	if err != nil {
		t.Fatalf("newQueryResultBudget() error = %v", err)
	}

	rowSize, fits, err := encodedRawRowSize([][]byte{[]byte("value")}, 1<<20)
	if err != nil || !fits {
		t.Fatalf("encodedRawRowSize() = %d, %t, %v", rowSize, fits, err)
	}

	if budget.FitsAdditionalRow(rowSize) {
		t.Fatal("FitsAdditionalRow() = true, want result-too-large")
	}
}

func TestInitialQueryRowCapacityIsBounded(t *testing.T) {
	t.Parallel()

	if got := initialQueryRowCapacity(&QueryExecutor{truncate: true, rowLimit: 50_000}); got != 1000 {
		t.Fatalf("initial capacity = %d, want 1000", got)
	}

	if got := initialQueryRowCapacity(&QueryExecutor{truncate: false, rowLimit: 50_000}); got != 0 {
		t.Fatalf("disabled initial capacity = %d, want 0", got)
	}
}

func budgetCaseName(truncated bool, count int) string {
	return fmt.Sprintf("truncated=%t/count=%d", truncated, count)
}
