// Package postgres normalizes PostgreSQL connection URIs for secure import.
package postgres

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/adamraziv/dataporch/internal/connection"
)

const (
	// Kind identifies the built-in PostgreSQL adapter.
	Kind connection.Kind = "postgres"

	settingDatabase = "database"
	settingHost     = "host"
	settingPort     = "port"
	settingSSLMode  = "sslmode"
	settingUsername = "username"
)

// ErrInvalidConnectionString identifies a rejected PostgreSQL connection URI.
var ErrInvalidConnectionString = errors.New("postgres: invalid connection string")

// Adapter normalizes PostgreSQL connection URIs without opening a connection.
type Adapter struct{}

var _ connection.Adapter = (*Adapter)(nil)

type connectionFields struct {
	username string
	password string
	host     string
	port     string
	database string
	sslMode  string
}

// New constructs a PostgreSQL import adapter.
func New() *Adapter {
	return &Adapter{}
}

// Kind returns the connection kind handled by the adapter.
func (*Adapter) Kind() connection.Kind {
	return Kind
}

// ParseConnectionString validates and normalizes an approved PostgreSQL URI.
func (*Adapter) ParseConnectionString(input []byte) (connection.ParsedConnection, error) {
	fields, err := parseConnectionURI(input)
	if err != nil {
		return connection.ParsedConnection{}, err
	}

	settings := map[string]string{
		settingUsername: fields.username,
		settingHost:     fields.host,
		settingDatabase: fields.database,
	}
	if fields.port != "" {
		settings[settingPort] = fields.port
	}

	if fields.sslMode != "" {
		settings[settingSSLMode] = fields.sslMode
	}

	return connection.ParsedConnection{
		Settings: settings,
		Secrets: map[string][]byte{
			"password": []byte(fields.password),
		},
	}, nil
}

func parseConnectionURI(input []byte) (connectionFields, error) {
	if bytes.IndexByte(input, '#') >= 0 {
		return connectionFields{}, invalidConnectionString("fragment not allowed")
	}

	uri, err := url.Parse(string(input))
	if err != nil {
		return connectionFields{}, invalidConnectionString("malformed uri")
	}

	if err := validateScheme(uri); err != nil {
		return connectionFields{}, err
	}

	username, password, err := parseCredentials(uri.User)
	if err != nil {
		return connectionFields{}, err
	}

	host, port, err := parseAddress(uri)
	if err != nil {
		return connectionFields{}, err
	}

	database, err := parseDatabase(uri)
	if err != nil {
		return connectionFields{}, err
	}

	sslMode, err := parseSSLMode(uri.RawQuery)
	if err != nil {
		return connectionFields{}, err
	}

	return connectionFields{
		username: username,
		password: password,
		host:     host,
		port:     port,
		database: database,
		sslMode:  sslMode,
	}, nil
}

func validateScheme(uri *url.URL) error {
	if uri.Opaque != "" {
		return invalidConnectionString("malformed uri")
	}

	switch uri.Scheme {
	case "postgres", "postgresql":
		return nil
	default:
		return invalidConnectionString("unsupported scheme")
	}
}

func parseCredentials(user *url.Userinfo) (string, string, error) {
	if user == nil {
		return "", "", invalidConnectionString("missing username")
	}

	username := user.Username()
	if username == "" {
		return "", "", invalidConnectionString("missing username")
	}

	password, exists := user.Password()
	if !exists || password == "" {
		return "", "", invalidConnectionString("missing password")
	}

	return username, password, nil
}

func parseAddress(uri *url.URL) (string, string, error) {
	if uri.Host == "" {
		return "", "", invalidConnectionString("missing host")
	}

	if strings.Contains(uri.Host, ",") {
		return "", "", invalidConnectionString("multiple hosts")
	}

	host := uri.Hostname()
	if host == "" {
		return "", "", invalidConnectionString("missing host")
	}

	port := uri.Port()
	if port == "" {
		if strings.HasSuffix(uri.Host, ":") {
			return "", "", invalidConnectionString("invalid port")
		}

		return host, "", nil
	}

	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", "", invalidConnectionString("invalid port")
	}

	return host, port, nil
}

func parseDatabase(uri *url.URL) (string, error) {
	escapedPath := uri.EscapedPath()
	if !strings.HasPrefix(escapedPath, "/") {
		return "", invalidConnectionString("missing database")
	}

	escapedDatabase := strings.TrimPrefix(escapedPath, "/")
	if escapedDatabase == "" {
		return "", invalidConnectionString("missing database")
	}

	if strings.Contains(escapedDatabase, "/") {
		return "", invalidConnectionString("multiple database path segments")
	}

	database, err := url.PathUnescape(escapedDatabase)
	if err != nil || database == "" {
		return "", invalidConnectionString("invalid database")
	}

	return database, nil
}

func parseSSLMode(rawQuery string) (string, error) {
	parameters, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", invalidConnectionString("malformed query")
	}

	if len(parameters) == 0 {
		return "", nil
	}

	values, exists := parameters[settingSSLMode]
	if !exists || len(parameters) != 1 {
		return "", invalidConnectionString("unsupported parameter")
	}

	if len(values) != 1 {
		return "", invalidConnectionString("repeated sslmode")
	}

	sslMode := values[0]
	if !validSSLMode(sslMode) {
		return "", invalidConnectionString("invalid sslmode")
	}

	return sslMode, nil
}

func validSSLMode(sslMode string) bool {
	switch sslMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}

func invalidConnectionString(category string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConnectionString, category)
}
