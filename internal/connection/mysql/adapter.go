package mysql

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/adamraziv/dataporch/internal/connection"
)

const (
	Kind connection.Kind = "mysql"

	settingDatabase = "database"
	settingHost     = "host"
	settingPort     = "port"
	settingSSLMode  = "sslmode"
	settingUsername = "username"
	settingPassword = "password"

	sslModeDisable    = "disable"
	sslModePrefer     = "prefer"
	sslModeRequire    = "require"
	sslModeVerifyFull = "verify-full"
)

var ErrInvalidConnectionString = errors.New("mysql: invalid connection string")

type Adapter struct{}

type connectionFields struct {
	username string
	password string
	host     string
	port     string
	database string
	sslMode  string
}

var _ connection.Adapter = (*Adapter)(nil)

func New() *Adapter { return &Adapter{} }

func (*Adapter) Kind() connection.Kind { return Kind }

func (*Adapter) ParseConnectionString(input []byte) (connection.ParsedConnection, error) {
	fields, err := parseConnectionURI(input)
	if err != nil {
		return connection.ParsedConnection{}, err
	}

	settings := map[string]string{
		settingUsername: strings.Clone(fields.username),
		settingHost:     strings.Clone(fields.host),
		settingDatabase: strings.Clone(fields.database),
	}
	if fields.port != "" {
		settings[settingPort] = strings.Clone(fields.port)
	}
	if fields.sslMode != "" {
		settings[settingSSLMode] = strings.Clone(fields.sslMode)
	}

	return connection.ParsedConnection{
		Settings: settings,
		Secrets: map[string][]byte{
			settingPassword: []byte(fields.password),
		},
	}, nil
}

func parseConnectionURI(input []byte) (connectionFields, error) {
	raw := string(input)
	if strings.Contains(raw, "#") {
		return connectionFields{}, invalidConnectionString("fragment is not allowed")
	}

	uri, err := url.Parse(raw)
	if err != nil {
		return connectionFields{}, invalidConnectionString("malformed uri")
	}
	if uri.Opaque != "" || uri.Scheme != string(Kind) {
		return connectionFields{}, invalidConnectionString("invalid scheme")
	}

	username, password, err := parseCredentials(uri)
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

func parseCredentials(uri *url.URL) (string, string, error) {
	if uri.User == nil {
		return "", "", invalidConnectionString("credentials are required")
	}

	username := uri.User.Username()
	password, hasPassword := uri.User.Password()
	if username == "" || !hasPassword || password == "" {
		return "", "", invalidConnectionString("username and password are required")
	}
	if strings.ContainsRune(username, '\x00') || strings.ContainsRune(password, '\x00') {
		return "", "", invalidConnectionString("credentials contain nul")
	}

	return username, password, nil
}

func parseAddress(uri *url.URL) (string, string, error) {
	if uri.Host == "" || strings.Contains(uri.Host, ",") {
		return "", "", invalidConnectionString("exactly one host is required")
	}

	host := uri.Hostname()
	if host == "" {
		return "", "", invalidConnectionString("host is required")
	}

	if strings.Count(uri.Host, ":") > 1 && !strings.HasPrefix(uri.Host, "[") {
		return "", "", invalidConnectionString("ipv6 host must be bracketed")
	}

	port := uri.Port()
	if port == "" {
		return host, "", nil
	}

	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return "", "", invalidConnectionString("invalid port")
	}

	return host, port, nil
}

func parseDatabase(uri *url.URL) (string, error) {
	escaped := strings.TrimPrefix(uri.EscapedPath(), "/")
	if escaped == "" || strings.Contains(escaped, "/") {
		return "", invalidConnectionString("exactly one database is required")
	}

	database, err := url.PathUnescape(escaped)
	if err != nil || database == "" || strings.ContainsRune(database, '\x00') {
		return "", invalidConnectionString("invalid database")
	}

	return database, nil
}

func parseSSLMode(rawQuery string) (string, error) {
	if rawQuery == "" {
		return "", nil
	}

	values, err := url.ParseQuery(rawQuery)
	if err != nil || len(values) != 1 {
		return "", invalidConnectionString("invalid query parameters")
	}

	modes, exists := values[settingSSLMode]
	if !exists || len(modes) != 1 {
		return "", invalidConnectionString("sslmode must appear exactly once")
	}

	switch modes[0] {
	case sslModeDisable, sslModePrefer, sslModeRequire, sslModeVerifyFull:
		return modes[0], nil
	default:
		return "", invalidConnectionString("unsupported sslmode")
	}
}

func invalidConnectionString(category string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConnectionString, category)
}
