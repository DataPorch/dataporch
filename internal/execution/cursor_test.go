package execution

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()

	request := cursorRequest{
		Operation:           "relational_database.list_columns",
		SourceID:            "analytics",
		Schema:              "Sales Data",
		Table:               "Customers",
		Limit:               7,
		Search:              `%_*."[x]\\`,
		IncludeDescriptions: true,
	}

	nameCursor, err := encodeCursor(request, "customers", 0)
	if err != nil {
		t.Fatalf("encodeCursor() error = %v", err)
	}

	namePayload, err := decodeCursor(nameCursor, request, false)
	if err != nil {
		t.Fatalf("decodeCursor(name) error = %v", err)
	}

	if namePayload.LastName != "customers" || namePayload.LastOrdinal != 0 {
		t.Fatalf("name payload = %#v, want customers", namePayload)
	}

	ordinalCursor, err := encodeCursor(request, "", 11)
	if err != nil {
		t.Fatalf("encodeCursor(ordinal) error = %v", err)
	}

	ordinalPayload, err := decodeCursor(ordinalCursor, request, true)
	if err != nil {
		t.Fatalf("decodeCursor(ordinal) error = %v", err)
	}

	if ordinalPayload.LastOrdinal != 11 || ordinalPayload.LastName != "" {
		t.Fatalf("ordinal payload = %#v, want ordinal 11", ordinalPayload)
	}
}

func TestCursorBindsEveryRequestField(t *testing.T) {
	t.Parallel()

	base := cursorRequest{
		Operation:           "relational_database.list_tables",
		SourceID:            "analytics",
		Schema:              "public",
		Table:               "",
		Limit:               10,
		Search:              "customer",
		IncludeDescriptions: false,
	}

	cursor, err := encodeCursor(base, "orders", 0)
	if err != nil {
		t.Fatalf("encodeCursor() error = %v", err)
	}

	fields := []struct {
		name   string
		mutate func(*cursorRequest)
	}{
		{name: "operation", mutate: func(request *cursorRequest) { request.Operation = "other" }},
		{name: "source", mutate: func(request *cursorRequest) { request.SourceID = "warehouse" }},
		{name: "schema", mutate: func(request *cursorRequest) { request.Schema = "sales" }},
		{name: "table", mutate: func(request *cursorRequest) { request.Table = "orders" }},
		{name: "limit", mutate: func(request *cursorRequest) { request.Limit = 11 }},
		{name: "search", mutate: func(request *cursorRequest) { request.Search = "order" }},
		{name: "descriptions", mutate: func(request *cursorRequest) { request.IncludeDescriptions = true }},
	}

	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			t.Parallel()

			mutated := base
			field.mutate(&mutated)

			if _, err := decodeCursor(cursor, mutated, false); !isInvalidCursor(err) {
				t.Fatalf("decodeCursor() error = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func TestCursorRejectsMalformedPayloads(t *testing.T) {
	t.Parallel()

	request := cursorRequest{Operation: "data_source.list", Limit: 3}

	valid, err := encodeCursor(request, "alpha", 0)
	if err != nil {
		t.Fatalf("encodeCursor() error = %v", err)
	}

	tests := []struct {
		name   string
		cursor string
	}{
		{name: "malformed base64", cursor: "!not-base64"},
		{name: "malformed json", cursor: base64.RawURLEncoding.EncodeToString([]byte("{"))},
		{name: "unknown field", cursor: encodePayload(t, map[string]any{"version": 1, "fingerprint": strings.Repeat("0", 64), "last_name": "alpha", "unknown": true})},
		{name: "trailing json", cursor: valid + base64.RawURLEncoding.EncodeToString([]byte(`{"version":1}`))},
		{name: "wrong hash", cursor: encodePayload(t, map[string]any{"version": 1, "fingerprint": strings.Repeat("0", 64), "last_name": "alpha"})},
		{name: "wrong key", cursor: encodePayload(t, map[string]any{"version": 1, "fingerprint": mustFingerprint(t, request), "last_ordinal": 3})},
		{name: "oversize", cursor: strings.Repeat("a", 8193)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := decodeCursor(tt.cursor, request, false); !isInvalidCursor(err) {
				t.Fatalf("decodeCursor() error = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func encodePayload(t *testing.T, value map[string]any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	return base64.RawURLEncoding.EncodeToString(encoded)
}

func mustFingerprint(t *testing.T, request cursorRequest) string {
	t.Helper()

	fingerprint, err := requestFingerprint(request)
	if err != nil {
		t.Fatalf("requestFingerprint() error = %v", err)
	}

	return fingerprint
}

func isInvalidCursor(err error) bool {
	return err != nil && errors.Is(err, ErrInvalidCursor)
}
