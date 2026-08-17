package sqlite

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
)

func TestBlobLiteral(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "empty", raw: []byte{}, want: "X''"},
		{name: "bytes", raw: []byte{0x00, 0x01, 0xab, 0xff}, want: "X'0001ABFF'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := blobLiteral(test.raw); got != test.want {
				t.Fatalf("blobLiteral(%#v) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestQueryResultBudgetMatchesEncodedResult(t *testing.T) {
	t.Parallel()

	result := execution.RelationalQueryResult{
		Kind:     Kind,
		SourceID: connection.ID("source"),
		Columns: []execution.RelationalQueryColumn{
			{Name: "value", DatabaseType: "TEXT"},
		},
		Rows: [][]*string{{stringPtr(`{"key":"value"}`)}, {nil}},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	budget, err := newQueryResultBudget(result, len(encoded))
	if err != nil {
		t.Fatalf("newQueryResultBudget() error = %v", err)
	}
	firstSize, fits, err := encodedRowSize(result.Rows[0], len(encoded))
	if err != nil || !fits || !budget.FitsAdditionalRow(firstSize) {
		t.Fatalf("first row budget = size %d fits %v err %v", firstSize, fits, err)
	}
	budget.RetainRow(firstSize)
	secondSize, fits, err := encodedRowSize(result.Rows[1], len(encoded))
	if err != nil || !fits || !budget.FitsAdditionalRow(secondSize) {
		t.Fatalf("second row budget = size %d fits %v err %v", secondSize, fits, err)
	}
	budget.RetainRow(secondSize)
	if budget.retainedRows != 2 {
		t.Fatalf("retained rows = %d, want 2", budget.retainedRows)
	}
}

func TestQueryResultBudgetRejectsMetadataAndRows(t *testing.T) {
	t.Parallel()

	result := execution.RelationalQueryResult{
		Kind:     Kind,
		SourceID: "source",
		Columns:  []execution.RelationalQueryColumn{{Name: "value", DatabaseType: "TEXT"}},
		Rows:     make([][]*string, 0),
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	if _, err := newQueryResultBudget(result, len(encoded)-1); !errors.Is(err, execution.ErrResultTooLarge) {
		t.Fatalf("metadata budget error = %v, want ErrResultTooLarge", err)
	}

	budget, err := newQueryResultBudget(result, len(encoded)+20)
	if err != nil {
		t.Fatalf("newQueryResultBudget() error = %v", err)
	}
	row := []*string{stringPtr("this row cannot fit")}
	rowSize, _, err := encodedRowSize(row, len(encoded)+20)
	if err != nil {
		t.Fatalf("encodedRowSize() error = %v", err)
	}
	if budget.FitsAdditionalRow(rowSize) {
		t.Fatal("FitsAdditionalRow() = true, want false")
	}
}

func TestEncodedRowSizePreservesNullsAndEscapes(t *testing.T) {
	t.Parallel()

	row := []*string{nil, stringPtr(`"quoted"`), stringPtr("")}
	size, fits, err := encodedRowSize(row, 1024)
	if err != nil || !fits {
		t.Fatalf("encodedRowSize() = size %d fits %v err %v", size, fits, err)
	}
	encoded := []byte{'['}
	for _, value := range row {
		if value == nil {
			encoded = append(encoded, []byte("null")...)
		} else {
			part, marshalErr := json.Marshal(*value)
			if marshalErr != nil {
				t.Fatalf("json.Marshal(value) error = %v", marshalErr)
			}
			encoded = append(encoded, part...)
		}
		encoded = append(encoded, ',')
	}
	encoded = encoded[:len(encoded)-1]
	encoded = append(encoded, ']')
	if size != len(encoded) {
		t.Fatalf("encoded row size = %d, want %d", size, len(encoded))
	}
}

func TestQueryResultBudgetRejectsInvalidRows(t *testing.T) {
	t.Parallel()

	result := execution.RelationalQueryResult{Kind: Kind, SourceID: "source"}
	budget, err := newQueryResultBudget(result, 1024)
	if err != nil {
		t.Fatalf("newQueryResultBudget() error = %v", err)
	}
	if _, fits, err := encodedRowSize(nil, 1024); err != nil || !fits {
		t.Fatalf("encodedRowSize(empty) = fits %v err %v, want fit", fits, err)
	}
	if reflect.DeepEqual(budget, queryResultBudget{}) {
		t.Fatal("budget is empty")
	}
}
