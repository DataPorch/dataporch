package mysql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	gomysql "github.com/go-sql-driver/mysql"
)

const (
	initialOpenTimeout    = 10 * time.Second
	metadataIOTimeout     = 20 * time.Second
	defaultPort           = uint16(3306)
	defaultSSLMode        = sslModePrefer
	maxOpenConnections    = 4
	maxIdleConnections    = 2
	maxConnectionLifetime = 30 * time.Minute
	maxConnectionIdleTime = 5 * time.Minute
)

var errInvalidRuntimeDefinition = errors.New("mysql: invalid runtime definition")

type catalogRows interface {
	Close() error
	Err() error
	Next() bool
	Scan(...any) error
}

type runtimePool interface {
	Ping(context.Context) error
	Query(context.Context, string, ...any) (catalogRows, error)
	Close() error
}

type poolFactory interface {
	New(context.Context, connection.ResolvedDefinition) (runtimePool, error)
}

type poolConfigurer interface {
	SetMaxOpenConns(int)
	SetMaxIdleConns(int)
	SetConnMaxLifetime(time.Duration)
	SetConnMaxIdleTime(time.Duration)
}

type runtimeSettings struct {
	username string
	password string
	host     string
	port     uint16
	database string
	sslMode  string
}

type mysqlRuntimePool struct {
	db *sql.DB
}

type sqlPoolFactory struct{}

var (
	_ runtimePool    = (*mysqlRuntimePool)(nil)
	_ poolFactory    = sqlPoolFactory{}
	_ poolConfigurer = (*sql.DB)(nil)
)

func newSQLPoolFactory() poolFactory {
	return sqlPoolFactory{}
}

func (sqlPoolFactory) New(ctx context.Context, definition connection.ResolvedDefinition) (runtimePool, error) {
	if ctx == nil {
		return nil, errInvalidRuntimeDefinition
	}

	settings, err := validateRuntimeDefinition(definition)
	if err != nil {
		return nil, err
	}

	config, err := driverConfig(settings)
	if err != nil {
		return nil, err
	}

	connector, err := gomysql.NewConnector(config)
	if err != nil {
		return nil, fmt.Errorf("%w: creating connector: %v", errInvalidRuntimeDefinition, err)
	}

	db := sql.OpenDB(connector)
	configurePool(db)

	return &mysqlRuntimePool{db: db}, nil
}

func (p *mysqlRuntimePool) Ping(ctx context.Context) error {
	if p == nil || p.db == nil {
		return errInvalidRuntimeDefinition
	}

	return p.db.PingContext(ctx)
}

func (p *mysqlRuntimePool) Query(
	ctx context.Context,
	query string,
	arguments ...any,
) (catalogRows, error) {
	if p == nil || p.db == nil {
		return nil, errInvalidRuntimeDefinition
	}

	return p.db.QueryContext(ctx, query, arguments...)
}

func (p *mysqlRuntimePool) Close() error {
	if p == nil || p.db == nil {
		return nil
	}

	return p.db.Close()
}

func validateRuntimeDefinition(definition connection.ResolvedDefinition) (runtimeSettings, error) {
	if definition.Kind != Kind {
		return runtimeSettings{}, errInvalidRuntimeDefinition
	}

	if err := (connection.Definition{ID: definition.ID, Kind: Kind}).Validate(); err != nil {
		return runtimeSettings{}, errInvalidRuntimeDefinition
	}

	settings, err := validateRuntimeSettings(definition.Settings)
	if err != nil {
		return runtimeSettings{}, err
	}

	password, err := validateRuntimePassword(definition.Secrets)
	if err != nil {
		return runtimeSettings{}, err
	}

	settings.password = password

	return settings, nil
}

func validateRuntimeSettings(values map[string]string) (runtimeSettings, error) {
	for name, value := range values {
		switch name {
		case settingUsername, settingHost, settingPort, settingDatabase, settingSSLMode:
			if !validRuntimeValue(value) {
				return runtimeSettings{}, errInvalidRuntimeDefinition
			}
		default:
			return runtimeSettings{}, errInvalidRuntimeDefinition
		}
	}

	username, ok := values[settingUsername]
	if !ok || !validRuntimeValue(username) {
		return runtimeSettings{}, errInvalidRuntimeDefinition
	}

	host, ok := values[settingHost]
	if !ok || !validRuntimeValue(host) || strings.ContainsAny(host, ",/") {
		return runtimeSettings{}, errInvalidRuntimeDefinition
	}

	database, ok := values[settingDatabase]
	if !ok || !validRuntimeValue(database) {
		return runtimeSettings{}, errInvalidRuntimeDefinition
	}

	port, err := runtimePort(values)
	if err != nil {
		return runtimeSettings{}, err
	}

	sslMode := defaultSSLMode
	if value, exists := values[settingSSLMode]; exists {
		if !isSupportedSSLMode(value) {
			return runtimeSettings{}, errInvalidRuntimeDefinition
		}
		sslMode = value
	}

	return runtimeSettings{
		username: username,
		host:     host,
		port:     port,
		database: database,
		sslMode:  sslMode,
	}, nil
}

func validateRuntimePassword(secrets map[string][]byte) (string, error) {
	if len(secrets) != 1 {
		return "", errInvalidRuntimeDefinition
	}

	password, ok := secrets[settingPassword]
	if !ok || !validRuntimeValue(string(password)) {
		return "", errInvalidRuntimeDefinition
	}

	return string(password), nil
}

func runtimePort(values map[string]string) (uint16, error) {
	portText, exists := values[settingPort]
	if !exists {
		return defaultPort, nil
	}

	parsed, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || parsed == 0 {
		return 0, errInvalidRuntimeDefinition
	}

	return uint16(parsed), nil
}

func validRuntimeValue(value string) bool {
	return value != "" && strings.IndexByte(value, '\x00') < 0
}

func isSupportedSSLMode(value string) bool {
	switch value {
	case sslModeDisable, sslModePrefer, sslModeRequire, sslModeVerifyFull:
		return true
	default:
		return false
	}
}

func driverConfig(settings runtimeSettings) (*gomysql.Config, error) {
	cfg := gomysql.NewConfig()
	cfg.User = settings.username
	cfg.Passwd = settings.password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(settings.host, strconv.Itoa(int(settings.port)))
	cfg.DBName = settings.database
	cfg.Logger = &gomysql.NopLogger{}
	cfg.MultiStatements = false
	cfg.AllowAllFiles = false
	cfg.AllowCleartextPasswords = false
	cfg.AllowOldPasswords = false
	cfg.InterpolateParams = false
	cfg.ParseTime = false
	cfg.Timeout = initialOpenTimeout
	cfg.ReadTimeout = metadataIOTimeout
	cfg.WriteTimeout = metadataIOTimeout

	tlsConfig, fallback, err := runtimeTLSConfig(settings.host, settings.sslMode)
	if err != nil {
		return nil, errInvalidRuntimeDefinition
	}
	cfg.TLS = tlsConfig
	cfg.AllowFallbackToPlaintext = fallback

	return cfg, nil
}

func runtimeTLSConfig(host, sslMode string) (*tls.Config, bool, error) {
	switch sslMode {
	case sslModeDisable:
		return nil, false, nil
	case sslModePrefer:
		return &tls.Config{ //nolint:gosec // prefer intentionally permits unverified TLS before plaintext fallback.
			ServerName:         host,
			InsecureSkipVerify: true,
		}, true, nil
	case sslModeRequire:
		return &tls.Config{ //nolint:gosec // require intentionally encrypts without identity verification by contract.
			ServerName:         host,
			InsecureSkipVerify: true,
		}, false, nil
	case sslModeVerifyFull:
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			return nil, false, fmt.Errorf("loading system certificate pool: %w", err)
		}
		return &tls.Config{
			ServerName: host,
			RootCAs:    roots,
			MinVersion: tls.VersionTLS12,
		}, false, nil
	default:
		return nil, false, errInvalidRuntimeDefinition
	}
}

func configurePool(db poolConfigurer) {
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxIdleConnections)
	db.SetConnMaxLifetime(maxConnectionLifetime)
	db.SetConnMaxIdleTime(maxConnectionIdleTime)
}
