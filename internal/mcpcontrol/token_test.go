package mcpcontrol

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestGenerateAndVerify(t *testing.T) {
	t.Parallel()

	credential, err := Generate(bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(credential) != 43 || strings.Contains(credential, "=") {
		t.Fatalf("Generate() = %q; want 43 unpadded base64url characters", credential)
	}
	if err := Verify(credential, credential); err != nil {
		t.Fatalf("Verify(equal) error = %v", err)
	}
	if err := Verify(credential, strings.Repeat("A", 43)); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("Verify(wrong) error = %v, want ErrInvalidCredential", err)
	}
}

func TestGenerateRejectsShortRandomness(t *testing.T) {
	t.Parallel()

	if _, err := Generate(bytes.NewReader([]byte("short"))); err == nil {
		t.Fatal("Generate(short randomness) error = nil, want non-nil")
	}
}

func TestValidateRejectsMalformedCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		credential string
	}{
		{name: "empty", credential: ""},
		{name: "short", credential: strings.Repeat("A", 42)},
		{name: "long", credential: strings.Repeat("A", 44)},
		{name: "invalid alphabet", credential: strings.Repeat("!", 43)},
		{name: "padded", credential: strings.Repeat("A", 42) + "="},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := Validate(test.credential); !errors.Is(err, ErrInvalidCredential) {
				t.Fatalf("Validate() error = %v, want ErrInvalidCredential", err)
			}
		})
	}
}
