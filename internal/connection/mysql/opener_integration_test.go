//go:build integration

package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/DataPorch/dataporch/internal/connection"
	"github.com/DataPorch/dataporch/internal/execution"
	"github.com/DataPorch/dataporch/internal/secret"
)

func testAdminDB(t *testing.T) *sql.DB {
	t.Helper()

	raw := os.Getenv("DATAPORCH_TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("DATAPORCH_TEST_MYSQL_DSN is not configured")
	}

	fields, err := parseConnectionURI([]byte(raw))
	if err != nil {
		t.Fatalf("parseConnectionURI() error = %v", err)
	}

	port := defaultPort
	if fields.port != "" {
		parsed, err := strconv.ParseUint(fields.port, 10, 16)
		if err != nil {
			t.Fatalf("parsing test port: %v", err)
		}

		port = uint16(parsed)
	}

	sslMode := fields.sslMode
	if sslMode == "" {
		sslMode = defaultSSLMode
	}

	cfg, err := driverConfig(runtimeSettings{
		username: fields.username,
		password: fields.password,
		host:     fields.host,
		port:     port,
		database: fields.database,
		sslMode:  sslMode,
	})
	if err != nil {
		t.Fatalf("driverConfig() error = %v", err)
	}

	connector, err := gomysql.NewConnector(cfg)
	if err != nil {
		t.Fatalf("NewConnector() error = %v", err)
	}

	db := sql.OpenDB(connector)

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("admin db Close() error = %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("admin db PingContext() error = %v", err)
	}

	return db
}

func testSuffix(t *testing.T) string {
	t.Helper()

	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}

	return hex.EncodeToString(raw)
}

func testIdentifier(t *testing.T, prefix string) string {
	t.Helper()

	value := prefix + "_" + testSuffix(t)
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			t.Fatalf("unsafe generated identifier %q", value)
		}
	}

	return value
}

func testQuotedIdentifier(t *testing.T, value string) string {
	t.Helper()

	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			t.Fatalf("unsafe generated identifier %q", value)
		}
	}

	return "`" + value + "`"
}

func testQuotedLiteral(t *testing.T, value string) string {
	t.Helper()

	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			t.Fatalf("unsafe generated literal %q", value)
		}
	}

	return "'" + value + "'"
}

func testExecSQL(t *testing.T, db *sql.DB, query string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if _, err := db.ExecContext(ctx, query); err != nil {
		t.Fatalf("ExecContext(%q) error = %v", query, err)
	}
}

type mysqlIntegrationFixture struct {
	admin       *sql.DB
	fields      connectionFields
	primaryDB   string
	secondaryDB string
	reader      string
	password    string
}

func newMySQLIntegrationFixture(t *testing.T) *mysqlIntegrationFixture {
	t.Helper()

	raw := os.Getenv("DATAPORCH_TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("DATAPORCH_TEST_MYSQL_DSN is not configured")
	}

	fields, err := parseConnectionURI([]byte(raw))
	if err != nil {
		t.Fatalf("parseConnectionURI() error = %v", err)
	}

	fixture := &mysqlIntegrationFixture{
		admin:       testAdminDB(t),
		fields:      fields,
		primaryDB:   testIdentifier(t, "primary"),
		secondaryDB: testIdentifier(t, "secondary"),
		reader:      testIdentifier(t, "reader"),
		password:    testIdentifier(t, "password"),
	}

	testExecSQL(t, fixture.admin, "CREATE DATABASE "+testQuotedIdentifier(t, fixture.primaryDB))
	testExecSQL(t, fixture.admin, "CREATE DATABASE "+testQuotedIdentifier(t, fixture.secondaryDB))
	testExecSQL(t, fixture.admin, fmt.Sprintf(
		"CREATE USER %s@'%%' IDENTIFIED BY %s",
		testQuotedLiteral(t, fixture.reader), testQuotedLiteral(t, fixture.password),
	))

	for _, database := range []string{fixture.primaryDB, fixture.secondaryDB} {
		testExecSQL(t, fixture.admin, fmt.Sprintf(
			"GRANT SELECT ON %s.* TO %s@'%%'",
			testQuotedIdentifier(t, database), testQuotedLiteral(t, fixture.reader),
		))
	}

	t.Cleanup(func() {
		_, _ = fixture.admin.ExecContext(context.Background(), fmt.Sprintf(
			"DROP USER IF EXISTS %s@'%%'", testQuotedLiteral(t, fixture.reader),
		))
		_, _ = fixture.admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+testQuotedIdentifier(t, fixture.primaryDB))
		_, _ = fixture.admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+testQuotedIdentifier(t, fixture.secondaryDB))
	})

	return fixture
}

func (fixture *mysqlIntegrationFixture) readerURI(t *testing.T, database, password string) string {
	t.Helper()

	port := fixture.fields.port
	if port == "" {
		port = strconv.Itoa(int(defaultPort))
	}

	uri := &url.URL{
		Scheme: string(Kind),
		User:   url.UserPassword(fixture.reader, password),
		Host:   net.JoinHostPort(fixture.fields.host, port),
		Path:   "/" + database,
	}
	if fixture.fields.sslMode != "" {
		query := url.Values{}
		query.Set(settingSSLMode, fixture.fields.sslMode)
		uri.RawQuery = query.Encode()
	}

	return uri.String()
}

type integrationSecretResolver struct{ password []byte }

func (resolver *integrationSecretResolver) Resolve(context.Context, secret.Reference) ([]byte, error) {
	return append([]byte(nil), resolver.password...), nil
}

func newMySQLIntegrationOpener(
	t *testing.T,
	id connection.ID,
	uri string,
) *Opener {
	t.Helper()

	parsed, err := New().ParseConnectionString([]byte(uri))
	if err != nil {
		t.Fatalf("ParseConnectionString() error = %v", err)
	}

	password := append([]byte(nil), parsed.Secrets[settingPassword]...)
	clear(parsed.Secrets[settingPassword])

	passwordRef, err := secret.NewLocal("mysql-integration-password")
	if err != nil {
		t.Fatalf("secret.NewLocal() error = %v", err)
	}

	definition := connection.Definition{
		ID:         id,
		Kind:       Kind,
		Settings:   parsed.Settings,
		SecretRefs: map[string]secret.Reference{settingPassword: passwordRef},
	}

	manager, err := connection.NewManager(&integrationSecretResolver{password: password}, []connection.Definition{definition})
	if err != nil {
		t.Fatalf("connection.NewManager() error = %v", err)
	}

	t.Cleanup(func() { clear(password) })

	opener, err := NewOpener(manager)
	if err != nil {
		t.Fatalf("NewOpener() error = %v", err)
	}

	t.Cleanup(func() { _ = opener.Close(context.Background()) })

	return opener
}

func TestOpenerMySQLIntegration(t *testing.T) {
	t.Parallel()

	fixture := newMySQLIntegrationFixture(t)
	opener := newMySQLIntegrationOpener(t, "mysql_primary", fixture.readerURI(t, fixture.primaryDB, fixture.password))

	first, err := opener.Open(t.Context(), "mysql_primary")
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}

	second, err := opener.Open(t.Context(), "mysql_primary")
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}

	if first != second {
		t.Fatal("runtime was not reused")
	}

	opener.Invalidate("mysql_primary")

	third, err := opener.Open(t.Context(), "mysql_primary")
	if err != nil {
		t.Fatalf("Open() after invalidation error = %v", err)
	}

	if third == first {
		t.Fatal("invalidation reused stale client")
	}

	if err := opener.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := opener.Open(t.Context(), "mysql_primary"); !errors.Is(err, ErrRuntimeClosed) {
		t.Fatalf("Open() after Close error = %v", err)
	}
}

func TestOpenerMySQLFailuresIntegration(t *testing.T) {
	t.Parallel()

	fixture := newMySQLIntegrationFixture(t)

	wrongPassword := fixture.password + "bad"
	authOpener := newMySQLIntegrationOpener(t, "mysql_auth", fixture.readerURI(t, fixture.primaryDB, wrongPassword))
	_, err := authOpener.OpenQuery(t.Context(), "mysql_auth")

	failure := execution.ClassifyRelationalQuery(t.Context(), err)
	if failure.Category != execution.ErrorCategoryDatabaseAuthenticationFailed {
		t.Fatalf("auth failure = %#v", failure)
	}

	unavailableURI := fixture.readerURI(t, fixture.primaryDB, fixture.password)

	parsed, err := url.Parse(unavailableURI)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	parsed.Host = net.JoinHostPort(fixture.fields.host, "1")
	unavailable := newMySQLIntegrationOpener(t, "mysql_unavailable", parsed.String())
	_, err = unavailable.OpenQuery(t.Context(), "mysql_unavailable")

	failure = execution.ClassifyRelationalQuery(t.Context(), err)
	if failure.Category != execution.ErrorCategoryDatabaseUnavailable {
		t.Fatalf("unavailable failure = %#v", failure)
	}
}
