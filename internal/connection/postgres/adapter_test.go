package postgres

import (
	"bytes"
	"errors"
	"maps"
	"strings"
	"testing"
)

func TestAdapterKind(t *testing.T) {
	t.Parallel()

	if got := New().Kind(); got != Kind {
		t.Fatalf("Kind() = %q, want %q", got, Kind)
	}
}

func TestParseConnectionStringNormalizesURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		connectionString string
		wantSettings     map[string]string
		wantPassword     string
	}{
		{
			name: "postgresql scheme with Supabase-style port",
			connectionString: "postgresql://postgres.obovklvzufwjjmjtvogd:decoded-password@" +
				"aws-1-ap-southeast-1.pooler.supabase.com:6543/postgres",
			wantSettings: map[string]string{
				"username": "postgres.obovklvzufwjjmjtvogd",
				"host":     "aws-1-ap-southeast-1.pooler.supabase.com",
				"port":     "6543",
				"database": "postgres",
			},
			wantPassword: "decoded-password",
		},
		{
			name:             "postgres scheme without port",
			connectionString: "postgres://reader:secret@db.example/finance",
			wantSettings: map[string]string{
				"username": "reader",
				"host":     "db.example",
				"database": "finance",
			},
			wantPassword: "secret",
		},
		{
			name:             "percent-encoded credentials and database",
			connectionString: "postgresql://reader%2Eservice:p%40ss%3Aword@db.example/finance%2Farchive",
			wantSettings: map[string]string{
				"username": "reader.service",
				"host":     "db.example",
				"database": "finance/archive",
			},
			wantPassword: "p@ss:word",
		},
		{
			name:             "bracketed IPv6 host",
			connectionString: "postgresql://reader:secret@[2001:db8::1]:6543/finance",
			wantSettings: map[string]string{
				"username": "reader",
				"host":     "2001:db8::1",
				"port":     "6543",
				"database": "finance",
			},
			wantPassword: "secret",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := []byte(test.connectionString)
			original := bytes.Clone(input)

			parsed, err := New().ParseConnectionString(input)
			if err != nil {
				t.Fatal("ParseConnectionString() rejected a valid URI")
			}

			if !maps.Equal(parsed.Settings, test.wantSettings) {
				t.Fatal("Settings do not match the expected normalized values")
			}

			password, exists := parsed.Secrets["password"]
			if len(parsed.Secrets) != 1 || !exists {
				t.Fatal("Secrets must contain password only")
			}

			wantPassword := []byte(test.wantPassword)
			defer clear(wantPassword)

			if !bytes.Equal(password, wantPassword) {
				t.Fatal("password secret does not match decoded password")
			}

			for name, value := range parsed.Settings {
				if value == test.wantPassword {
					t.Fatalf("Settings[%q] contains the password", name)
				}
			}

			if !bytes.Equal(input, original) {
				t.Fatal("ParseConnectionString() mutated its input")
			}
		})
	}
}

func TestParseConnectionStringAcceptsSupportedSSLModes(t *testing.T) {
	t.Parallel()

	sslModes := []string{
		"disable",
		"allow",
		"prefer",
		"require",
		"verify-ca",
		"verify-full",
	}

	for _, sslMode := range sslModes {
		t.Run(sslMode, func(t *testing.T) {
			t.Parallel()

			parsed, err := New().ParseConnectionString([]byte(
				"postgresql://reader:secret@db.example/finance?sslmode=" + sslMode,
			))
			if err != nil {
				t.Fatal("ParseConnectionString() rejected a supported sslmode")
			}

			if got := parsed.Settings["sslmode"]; got != sslMode {
				t.Fatalf("sslmode = %q, want %q", got, sslMode)
			}
		})
	}
}

func TestParseConnectionStringRejectsInvalidURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		connectionString string
	}{
		{name: "empty", connectionString: ""},
		{name: "unsupported scheme", connectionString: "mysql://reader:secret@db.example/finance"},
		{name: "keyword value", connectionString: "host=db.example user=reader password=secret dbname=finance"},
		{name: "opaque URI", connectionString: "postgresql:reader:secret@db.example/finance"},
		{name: "missing username", connectionString: "postgresql://:secret@db.example/finance"},
		{name: "missing password", connectionString: "postgresql://reader@db.example/finance"},
		{name: "empty password", connectionString: "postgresql://reader:@db.example/finance"},
		{name: "missing host", connectionString: "postgresql://reader:secret@/finance"},
		{name: "missing database", connectionString: "postgresql://reader:secret@db.example/"},
		{name: "multiple database segments", connectionString: "postgresql://reader:secret@db.example/a/b"},
		{name: "multiple hosts", connectionString: "postgresql://reader:secret@db1.example,db2.example/finance"},
		{
			name:             "Unix socket parameter",
			connectionString: "postgresql://reader:secret@/finance?host=%2Fvar%2Frun%2Fpostgresql",
		},
		{name: "fragment", connectionString: "postgresql://reader:secret@db.example/finance#section"},
		{name: "empty fragment", connectionString: "postgresql://reader:secret@db.example/finance#"},
		{
			name:             "repeated sslmode",
			connectionString: "postgresql://reader:secret@db.example/finance?sslmode=require&sslmode=prefer",
		},
		{
			name:             "unsupported sslmode",
			connectionString: "postgresql://reader:secret@db.example/finance?sslmode=enabled",
		},
		{
			name:             "empty sslmode",
			connectionString: "postgresql://reader:secret@db.example/finance?sslmode=",
		},
		{
			name:             "unsupported parameter",
			connectionString: "postgresql://reader:secret@db.example/finance?application_name=dataporch",
		},
		{name: "malformed escape", connectionString: "postgresql://reader:p%ZZ@db.example/finance"},
		{name: "nonnumeric port", connectionString: "postgresql://reader:secret@db.example:abc/finance"},
		{name: "empty port", connectionString: "postgresql://reader:secret@db.example:/finance"},
		{name: "zero port", connectionString: "postgresql://reader:secret@db.example:0/finance"},
		{name: "out-of-range port", connectionString: "postgresql://reader:secret@db.example:70000/finance"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := New().ParseConnectionString([]byte(test.connectionString))
			if !errors.Is(err, ErrInvalidConnectionString) {
				t.Fatal("error is not classifiable as ErrInvalidConnectionString")
			}

			if parsed.Settings != nil || parsed.Secrets != nil {
				t.Fatal("ParseConnectionString() returned a non-zero result on failure")
			}
		})
	}
}

func TestParseConnectionStringDoesNotExposeInputInErrors(t *testing.T) {
	t.Parallel()

	const (
		username = "username-canary-7a92"
		password = "password-canary-91f7c2"
		host     = "host-canary.invalid"
		database = "database-canary-42"
		port     = "6543"
	)

	connectionString := "postgresql://" + username + ":" + password + "@" + host + ":" + port + "/" +
		database + "?application_name=parameter-canary"

	_, err := New().ParseConnectionString([]byte(connectionString))
	if !errors.Is(err, ErrInvalidConnectionString) {
		t.Fatal("error is not classifiable as ErrInvalidConnectionString")
	}

	for _, sensitiveValue := range []string{connectionString, username, password, host, database, port, "parameter-canary"} {
		if strings.Contains(err.Error(), sensitiveValue) {
			t.Fatal("parser error exposed connection input")
		}
	}
}

func TestParseConnectionStringReturnsIndependentResults(t *testing.T) {
	t.Parallel()

	connectionString := []byte("postgresql://reader:secret@db.example/finance")

	first, err := New().ParseConnectionString(connectionString)
	if err != nil {
		t.Fatal("first ParseConnectionString() rejected a valid URI")
	}

	second, err := New().ParseConnectionString(connectionString)
	if err != nil {
		t.Fatal("second ParseConnectionString() rejected a valid URI")
	}

	first.Settings["host"] = "changed.invalid"
	first.Secrets["password"][0] = 'X'

	if second.Settings["host"] != "db.example" {
		t.Fatal("settings map is shared between parse results")
	}

	if !bytes.Equal(second.Secrets["password"], []byte("secret")) {
		t.Fatal("password bytes are shared between parse results")
	}
}
