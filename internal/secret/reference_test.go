package secret

import "testing"

func TestReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		scheme  string
		locator string
		wantErr bool
	}{
		{name: "local", raw: "local://abc123", scheme: "local", locator: "abc123"},
		{name: "future provider", raw: "aws-sm://finance/password", scheme: "aws-sm", locator: "finance/password"},
		{name: "missing scheme", raw: "abc123", wantErr: true},
		{name: "empty locator", raw: "local://", wantErr: true},
		{name: "uppercase scheme", raw: "LOCAL://abc123", wantErr: true},
		{name: "whitespace", raw: "local://abc 123", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ref, err := Parse(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Parse() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			scheme, locator, err := ref.Parts()
			if err != nil {
				t.Fatalf("Parts() error = %v", err)
			}
			if scheme != tt.scheme || locator != tt.locator {
				t.Fatalf("Parts() = %q, %q; want %q, %q", scheme, locator, tt.scheme, tt.locator)
			}
		})
	}
}

func TestNewLocalRejectsInvalidIdentifier(t *testing.T) {
	t.Parallel()

	tests := []string{"", "contains space", "nested/value"}
	for _, identifier := range tests {
		t.Run(identifier, func(t *testing.T) {
			t.Parallel()

			if _, err := NewLocal(identifier); err == nil {
				t.Fatal("NewLocal() error = nil, want non-nil")
			}
		})
	}
}
