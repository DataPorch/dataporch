package sqlite

import (
	"bytes"
	"errors"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdapterKind(t *testing.T) {
	t.Parallel()

	if got := New().Kind(); got != Kind {
		t.Fatalf("Kind() = %q, want %q", got, Kind)
	}
}

func TestParseConnectionStringNormalizesOfflinePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		uri  string
		path string
	}{
		{name: "absolute", uri: "sqlite:///tmp/dataporch.db", path: "/tmp/dataporch.db"},
		{name: "cleaned", uri: "sqlite:///tmp/./data/../dataporch.db", path: "/tmp/dataporch.db"},
		{name: "escaped space", uri: "sqlite:///tmp/data%20set/dataporch.db", path: "/tmp/data set/dataporch.db"},
		{name: "decode exactly once", uri: "sqlite:///tmp/%252Fdataporch.db", path: "/tmp/%2Fdataporch.db"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := []byte(test.uri)
			original := bytes.Clone(input)

			parsed, err := New().ParseConnectionString(input)
			if err != nil {
				t.Fatalf("ParseConnectionString() error = %v", err)
			}

			if len(parsed.Settings) != 0 {
				t.Fatalf("Settings = %#v, want no plaintext path settings", parsed.Settings)
			}

			pathSecret, exists := parsed.Secrets[secretPath]
			if len(parsed.Secrets) != 1 || !exists || string(pathSecret) != test.path {
				t.Fatalf("Secrets = %#v, want encrypted-boundary path %q", parsed.Secrets, test.path)
			}

			if !bytes.Equal(input, original) {
				t.Fatal("ParseConnectionString() mutated its input")
			}
		})
	}
}

func TestParseConnectionStringDoesNotTouchFilesystem(t *testing.T) {
	t.Parallel()

	nonexistent := filepath.Join(t.TempDir(), "missing", "dataporch.db")
	uri := "sqlite://" + (&url.URL{Path: nonexistent}).EscapedPath()

	parsed, err := New().ParseConnectionString([]byte(uri))
	if err != nil {
		t.Fatalf("ParseConnectionString() error = %v", err)
	}

	if _, err := os.Stat(nonexistent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parser touched %q: stat error = %v", nonexistent, err)
	}
	if string(parsed.Secrets[secretPath]) != nonexistent {
		t.Fatalf("path secret = %q, want %q", parsed.Secrets[secretPath], nonexistent)
	}
}

func TestParseConnectionStringRejectsInvalidURI(t *testing.T) {
	t.Parallel()

	canary := "sqlite:///tmp/sqlite-path-canary.db"
	tests := []struct {
		name string
		uri  string
	}{
		{name: "empty", uri: ""},
		{name: "unsupported scheme", uri: "postgres:///tmp/dataporch.db"},
		{name: "opaque", uri: "sqlite:tmp/dataporch.db"},
		{name: "missing literal triple slash", uri: "sqlite:/tmp/dataporch.db"},
		{name: "missing path", uri: "sqlite:///"},
		{name: "authority", uri: "sqlite://host/tmp/dataporch.db"},
		{name: "userinfo", uri: "sqlite://user@/tmp/dataporch.db"},
		{name: "query", uri: "sqlite:///tmp/dataporch.db?mode=ro"},
		{name: "empty query", uri: "sqlite:///tmp/dataporch.db?"},
		{name: "fragment", uri: "sqlite:///tmp/dataporch.db#fragment"},
		{name: "empty fragment", uri: "sqlite:///tmp/dataporch.db#"},
		{name: "nul", uri: "sqlite:///tmp/sqlite%00path.db"},
		{name: "malformed escape", uri: "sqlite:///tmp/sqlite%ZZpath.db"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := New().ParseConnectionString([]byte(test.uri))
			if !errors.Is(err, ErrInvalidConnectionString) {
				t.Fatalf("error = %v, want ErrInvalidConnectionString", err)
			}
			if parsed.Settings != nil || parsed.Secrets != nil {
				t.Fatalf("failed parse returned %#v", parsed)
			}
			for _, value := range []string{test.uri, canary, "sqlite-path-canary"} {
				if value != "" && strings.Contains(err.Error(), value) {
					t.Fatalf("parser error leaked %q: %v", value, err)
				}
			}
		})
	}
}

func TestParseConnectionStringReturnsIndependentResults(t *testing.T) {
	t.Parallel()

	uri := []byte("sqlite:///tmp/dataporch.db")
	first, err := New().ParseConnectionString(uri)
	if err != nil {
		t.Fatalf("first ParseConnectionString() error = %v", err)
	}
	second, err := New().ParseConnectionString(uri)
	if err != nil {
		t.Fatalf("second ParseConnectionString() error = %v", err)
	}

	first.Secrets[secretPath][0] = 'X'
	if string(second.Secrets[secretPath]) != "/tmp/dataporch.db" {
		t.Fatalf("secret bytes are shared: %q", second.Secrets[secretPath])
	}
	if !maps.Equal(first.Settings, second.Settings) {
		t.Fatalf("settings differ unexpectedly: %#v / %#v", first.Settings, second.Settings)
	}
}
