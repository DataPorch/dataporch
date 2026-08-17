package execution

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSQLiteMetadataJSON(t *testing.T) {
	t.Parallel()

	column := Column{
		Name:          "payload",
		FormattedType: "JSON TEXT",
		Type: DataType{
			Category: TypeCategoryDynamic,
			Affinity: TypeAffinityText,
		},
		Generated: &Generated{Kind: "virtual"},
	}
	constraint := Constraint{
		Kind:    "primary_key",
		Columns: []string{"id"},
	}

	encoded, err := json.Marshal(struct {
		Column     Column     `json:"column"`
		Constraint Constraint `json:"constraint"`
	}{
		Column:     column,
		Constraint: constraint,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	for _, want := range []string{
		`"category":"dynamic"`,
		`"affinity":"text"`,
		`"kind":"virtual"`,
		`"kind":"primary_key"`,
	} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Fatalf("metadata JSON %q missing %q", encoded, want)
		}
	}

	if bytes.Contains(encoded, []byte(`"expression"`)) {
		t.Fatalf("metadata JSON %q unexpectedly contains generated expression", encoded)
	}

	var decoded struct {
		Constraint map[string]any `json:"constraint"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if _, exists := decoded.Constraint["name"]; exists {
		t.Fatalf("constraint JSON unexpectedly contains name: %q", encoded)
	}
}
