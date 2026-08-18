package mysql

import (
	"errors"
	"reflect"
	"testing"
)

func TestAdapterParseConnectionString(t *testing.T) {
	t.Parallel()

	adapter := New()

	parsed, err := adapter.ParseConnectionString([]byte(
		"mysql://reader:secret@db.example.com:3307/finance?sslmode=verify-full",
	))
	if err != nil {
		t.Fatalf("ParseConnectionString() error = %v", err)
	}

	wantSettings := map[string]string{
		settingUsername: "reader",
		settingHost:     "db.example.com",
		settingPort:     "3307",
		settingDatabase: "finance",
		settingSSLMode:  "verify-full",
	}
	if !reflect.DeepEqual(parsed.Settings, wantSettings) {
		t.Fatalf("Settings = %#v, want %#v", parsed.Settings, wantSettings)
	}

	if got := string(parsed.Secrets[settingPassword]); got != "secret" {
		t.Fatalf("password = %q, want %q", got, "secret")
	}
}

func TestAdapterParseConnectionStringRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "wrong scheme", input: "postgres://reader:secret@db.example.com/finance"},
		{name: "empty scheme", input: "//reader:secret@db.example.com/finance"},
		{name: "opaque uri", input: "mysql:reader:secret@db.example.com/finance"},
		{name: "missing username", input: "mysql://:secret@db.example.com/finance"},
		{name: "missing password", input: "mysql://reader@db.example.com/finance"},
		{name: "missing host", input: "mysql://reader:secret@/finance"},
		{name: "missing database", input: "mysql://reader:secret@db.example.com/"},
		{
			name:  "comma separated hosts",
			input: "mysql://reader:secret@db1.example.com,db2.example.com/finance",
		},
		{name: "unbracketed ipv6", input: "mysql://reader:secret@2001:db8::1/finance"},
		{name: "invalid port", input: "mysql://reader:secret@db.example.com:notaport/finance"},
		{name: "zero port", input: "mysql://reader:secret@db.example.com:0/finance"},
		{name: "extra path segment", input: "mysql://reader:secret@db.example.com/finance/archive"},
		{
			name:  "malformed username encoding",
			input: "mysql://read%ZZer:secret@db.example.com/finance",
		},
		{
			name:  "malformed database encoding",
			input: "mysql://reader:secret@db.example.com/fin%ZZance",
		},
		{name: "nul username", input: "mysql://%00reader:secret@db.example.com/finance"},
		{name: "nul password", input: "mysql://reader:secret%00@db.example.com/finance"},
		{name: "nul database", input: "mysql://reader:secret@db.example.com/finance%00"},
		{name: "fragment", input: "mysql://reader:secret@db.example.com/finance#fragment"},
		{
			name:  "unknown parameter",
			input: "mysql://reader:secret@db.example.com/finance?charset=utf8mb4",
		},
		{
			name:  "repeated sslmode",
			input: "mysql://reader:secret@db.example.com/finance?sslmode=require&sslmode=disable",
		},
		{
			name:  "unsupported allow",
			input: "mysql://reader:secret@db.example.com/finance?sslmode=allow",
		},
		{
			name:  "unsupported verify ca",
			input: "mysql://reader:secret@db.example.com/finance?sslmode=verify-ca",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := New().ParseConnectionString([]byte(test.input))
			if !errors.Is(err, ErrInvalidConnectionString) {
				t.Fatalf("ParseConnectionString() error = %v, want %v", err, ErrInvalidConnectionString)
			}
		})
	}
}

func TestAdapterParseConnectionStringOmitsRuntimeDefaults(t *testing.T) {
	t.Parallel()

	parsed, err := New().ParseConnectionString([]byte(
		"mysql://reader:secret@db.example.com/finance",
	))
	if err != nil {
		t.Fatalf("ParseConnectionString() error = %v", err)
	}

	if _, exists := parsed.Settings[settingPort]; exists {
		t.Fatal("port must remain absent when omitted")
	}

	if _, exists := parsed.Settings[settingSSLMode]; exists {
		t.Fatal("sslmode must remain absent when omitted")
	}
}
