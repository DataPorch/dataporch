//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/DataPorch/dataporch/internal/connection"
	"github.com/DataPorch/dataporch/internal/secret"
)

func TestOpenerPostgresIntegration(t *testing.T) {
	t.Parallel()

	dsn := os.Getenv("DATAPORCH_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DATAPORCH_TEST_POSTGRES_DSN is not set")
	}

	definition, password := integrationDefinition(t, dsn)

	resolver := &integrationSecretResolver{password: password}

	t.Cleanup(func() {
		resolver.clearReturned()
		clear(password)
	})

	opener := newIntegrationOpener(t, resolver, definition)
	t.Cleanup(func() { _ = opener.Close(context.Background()) })

	first := openIntegrationClient(t, opener, definition.ID, "first")
	second := openIntegrationClient(t, opener, definition.ID, "second")
	assertSameIntegrationClient(t, first, second)

	invalidPassword := []byte("invalid-credential-canary")
	invalidResolver := &integrationSecretResolver{password: invalidPassword}

	t.Cleanup(func() {
		invalidResolver.clearReturned()
		clear(invalidPassword)
	})
	invalidOpener := newIntegrationOpener(t, invalidResolver, definition)
	t.Cleanup(func() { _ = invalidOpener.Close(context.Background()) })

	assertInvalidCredential(t, invalidOpener, definition.ID, invalidPassword, dsn)

	if err := opener.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	assertClosedIntegrationPool(t, first)
}

func integrationDefinition(t *testing.T, dsn string) (connection.Definition, []byte) {
	t.Helper()

	parsed, err := New().ParseConnectionString([]byte(dsn))
	if err != nil {
		t.Fatalf("ParseConnectionString() error = %v", err)
	}

	password := append([]byte(nil), parsed.Secrets[settingPassword]...)
	clear(parsed.Secrets[settingPassword])
	delete(parsed.Secrets, settingPassword)

	passwordRef, err := secret.NewLocal("integration-password")
	if err != nil {
		t.Fatalf("secret.NewLocal() error = %v", err)
	}

	return connection.Definition{
		ID:       "integration",
		Kind:     Kind,
		Settings: parsed.Settings,
		SecretRefs: map[string]secret.Reference{
			settingPassword: passwordRef,
		},
	}, password
}

func newIntegrationOpener(
	t *testing.T,
	resolver connection.SecretResolver,
	definition connection.Definition,
) *Opener {
	t.Helper()

	manager, err := connection.NewManager(resolver, []connection.Definition{definition})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	opener, err := NewOpener(manager)
	if err != nil {
		t.Fatalf("NewOpener() error = %v", err)
	}

	return opener
}

func openIntegrationClient(t *testing.T, opener *Opener, id connection.ID, label string) *Client {
	t.Helper()

	client, err := opener.Open(t.Context(), id)
	if err != nil {
		t.Fatalf("%s Open() error = %v", label, err)
	}

	return client
}

func assertSameIntegrationClient(t *testing.T, first, second *Client) {
	t.Helper()

	if first != second {
		t.Fatal("second Open() returned a different client")
	}
}

func assertInvalidCredential(
	t *testing.T,
	opener *Opener,
	id connection.ID,
	password []byte,
	dsn string,
) {
	t.Helper()

	_, err := opener.Open(t.Context(), id)
	if err == nil {
		t.Fatal("invalid credential Open() error = nil")
	}

	if !errors.Is(err, connection.ErrDatabaseUnavailable) {
		t.Fatalf("invalid credential Open() error = %v, want unavailable", err)
	}

	if strings.Contains(err.Error(), string(password)) || strings.Contains(err.Error(), dsn) {
		t.Fatalf("invalid credential Open() error exposes sensitive input: %v", err)
	}
}

func assertClosedIntegrationPool(t *testing.T, client *Client) {
	t.Helper()

	if err := client.pool.Ping(t.Context()); err == nil {
		t.Fatal("client pool Ping() after Close() succeeded")
	}
}

type integrationSecretResolver struct {
	password []byte
	returned [][]byte
}

func (r *integrationSecretResolver) Resolve(context.Context, secret.Reference) ([]byte, error) {
	value := append([]byte(nil), r.password...)
	r.returned = append(r.returned, value)

	return value, nil
}

func (r *integrationSecretResolver) clearReturned() {
	for _, value := range r.returned {
		clear(value)
	}

	r.returned = nil
}
