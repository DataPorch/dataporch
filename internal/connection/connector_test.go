package connection

import "testing"

func TestConnectorResolvesParserAdapter(t *testing.T) {
	t.Parallel()

	adapter := &adapterStub{kind: "postgres", parsed: ParsedConnection{Settings: map[string]string{"host": "postgres.internal"}}}
	connector, err := New(adapter)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	resolved, err := connector.Resolve("postgres")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	parsed, err := resolved.ParseConnectionString([]byte("private-input"))
	if err != nil {
		t.Fatalf("ParseConnectionString() error = %v", err)
	}
	if parsed.Settings["host"] != "postgres.internal" {
		t.Errorf("ParseConnectionString().Settings = %#v", parsed.Settings)
	}
}

type adapterStub struct {
	kind   Kind
	parsed ParsedConnection
	err    error
}

func (a *adapterStub) Kind() Kind { return a.kind }

func (a *adapterStub) ParseConnectionString([]byte) (ParsedConnection, error) {
	return a.parsed.Clone(), a.err
}
