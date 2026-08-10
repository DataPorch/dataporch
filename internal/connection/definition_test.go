package connection

import (
	"testing"

	"github.com/adamraziv/dataporch/internal/secret"
)

func TestDefinitionValidate(t *testing.T) {
	t.Parallel()

	valid := Definition{
		ID:   "finance",
		Kind: "postgres",
		Settings: map[string]string{
			"host":     "postgres.internal",
			"database": "finance",
			"username": "app_reader",
		},
		SecretRefs: map[string]secret.Reference{
			"password": "local://secret-b",
		},
	}

	tests := []struct {
		name   string
		change func(*Definition)
	}{
		{name: "empty id", change: func(definition *Definition) { definition.ID = "" }},
		{name: "whitespace id", change: func(definition *Definition) { definition.ID = "finance team" }},
		{name: "invalid id character", change: func(definition *Definition) { definition.ID = "finance/2026" }},
		{name: "empty kind", change: func(definition *Definition) { definition.Kind = "" }},
		{name: "invalid setting name", change: func(definition *Definition) {
			definition.Settings = map[string]string{"host name": "postgres.internal"}
		}},
		{name: "invalid secret name", change: func(definition *Definition) {
			definition.SecretRefs = map[string]secret.Reference{"password/name": "local://secret-b"}
		}},
		{name: "overlapping names", change: func(definition *Definition) {
			definition.SecretRefs = map[string]secret.Reference{"host": "local://secret-b"}
		}},
		{name: "invalid secret reference", change: func(definition *Definition) {
			definition.SecretRefs = map[string]secret.Reference{"password": "not-a-reference"}
		}},
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			definition := valid.Clone()
			tt.change(&definition)

			if err := definition.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want non-nil")
			}
		})
	}
}

func TestDefinitionCloneDoesNotShareMaps(t *testing.T) {
	t.Parallel()

	definition := Definition{
		ID:       "finance",
		Kind:     "postgres",
		Settings: map[string]string{"host": "postgres.internal"},
		SecretRefs: map[string]secret.Reference{
			"password": "local://secret-b",
		},
	}

	clone := definition.Clone()
	clone.Settings["host"] = "changed.internal"
	clone.SecretRefs["password"] = "local://secret-c"

	if definition.Settings["host"] != "postgres.internal" {
		t.Errorf("source settings = %q, want postgres.internal", definition.Settings["host"])
	}

	if definition.SecretRefs["password"] != "local://secret-b" {
		t.Errorf("source secret reference = %q, want local://secret-b", definition.SecretRefs["password"])
	}
}
