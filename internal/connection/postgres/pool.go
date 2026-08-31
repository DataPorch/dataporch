package postgres

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DataPorch/dataporch/internal/connection"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	initialOpenTimeout = 10 * time.Second
	defaultPort        = "5432"
	defaultSSLMode     = sslModePrefer

	basePoolConfig = "host=localhost port=5432 dbname=dataporch " +
		"user=dataporch password=dataporch sslmode=disable " +
		"passfile='' servicefile='' sslcert='' sslkey='' sslrootcert='' " +
		"connect_timeout=10 target_session_attrs=any"
)

var (
	errInvalidRuntimeDefinition = errors.New("postgres: invalid runtime definition")

	// pgx reads process-wide PG* variables while parsing; serialize the scrub-and-restore window.
	postgresEnvironmentMu sync.Mutex
)

type runtimePool interface {
	Ping(context.Context) error
	Query(context.Context, string, ...any) (catalogRows, error)
	Close()
}

type catalogRows interface {
	Close()
	Err() error
	Next() bool
	Scan(...any) error
}

type pgxRuntimePool struct {
	pool *pgxpool.Pool
}

type poolFactory interface {
	New(context.Context, connection.ResolvedDefinition) (runtimePool, error)
}

type pgxPoolFactory struct {
	template *pgxpool.Config
}

var _ runtimePool = (*pgxRuntimePool)(nil)

func (p *pgxRuntimePool) Ping(ctx context.Context) error {
	return p.pool.Ping(ctx)
}

func (p *pgxRuntimePool) Query(ctx context.Context, query string, arguments ...any) (catalogRows, error) {
	//nolint:sqlclosecheck // The catalogRows caller owns and closes the returned rows.
	return p.pool.Query(ctx, query, arguments...)
}

func (p *pgxRuntimePool) Close() {
	p.pool.Close()
}

func newPGXPoolFactory() (*pgxPoolFactory, error) {
	postgresEnvironmentMu.Lock()
	defer postgresEnvironmentMu.Unlock()

	previous := make(map[string]environmentValue)

	var scrubErr error

	for _, name := range postgresEnvironmentVariables() {
		value, exists := os.LookupEnv(name)

		previous[name] = environmentValue{value: value, exists: exists}
		if err := os.Unsetenv(name); err != nil {
			scrubErr = errors.Join(scrubErr, err)
		}
	}

	template, parseErr := pgxpool.ParseConfig(basePoolConfig)

	restoreErr := restorePostgresEnvironment(previous)
	if scrubErr != nil || parseErr != nil || restoreErr != nil {
		return nil, errors.Join(errInvalidRuntimeDefinition, scrubErr, restoreErr)
	}

	return &pgxPoolFactory{template: template}, nil
}

type environmentValue struct {
	value  string
	exists bool
}

func postgresEnvironmentVariables() []string {
	return []string{
		"PGHOST",
		"PGPORT",
		"PGDATABASE",
		"PGUSER",
		"PGPASSWORD",
		"PGPASSFILE",
		"PGAPPNAME",
		"PGCONNECT_TIMEOUT",
		"PGSSLMODE",
		"PGSSLKEY",
		"PGSSLCERT",
		"PGSSLSNI",
		"PGSSLROOTCERT",
		"PGSSLPASSWORD",
		"PGSSLNEGOTIATION",
		"PGTARGETSESSIONATTRS",
		"PGSERVICE",
		"PGSERVICEFILE",
		"PGTZ",
		"PGOPTIONS",
		"PGMINPROTOCOLVERSION",
		"PGMAXPROTOCOLVERSION",
		"PGCHANNELBINDING",
		"PGREQUIREAUTH",
	}
}

func restorePostgresEnvironment(previous map[string]environmentValue) error {
	var restoreErr error

	for _, name := range postgresEnvironmentVariables() {
		value := previous[name]

		var err error
		if value.exists {
			err = os.Setenv(name, value.value)
		} else {
			err = os.Unsetenv(name)
		}

		if err != nil {
			restoreErr = errors.Join(restoreErr, err)
		}
	}

	return restoreErr
}

func (f *pgxPoolFactory) New(ctx context.Context, definition connection.ResolvedDefinition) (runtimePool, error) {
	if ctx == nil {
		return nil, errInvalidRuntimeDefinition
	}

	config, err := f.config(definition)
	if err != nil {
		return nil, err
	}

	pools, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errInvalidRuntimeDefinition
	}

	return &pgxRuntimePool{pool: pools}, nil
}

func (f *pgxPoolFactory) config(definition connection.ResolvedDefinition) (*pgxpool.Config, error) {
	settings, err := validateRuntimeDefinition(definition)
	if err != nil {
		return nil, err
	}

	if f == nil || f.template == nil || f.template.ConnConfig == nil {
		return nil, errInvalidRuntimeDefinition
	}

	tlsConfigs, err := tlsConfigs(settings.host, settings.sslMode)
	if err != nil {
		return nil, err
	}

	config := f.template.Copy()
	config.ConnConfig.Host = settings.host
	config.ConnConfig.Port = settings.port
	config.ConnConfig.Database = settings.database
	config.ConnConfig.User = settings.username
	config.ConnConfig.Password = settings.password
	config.ConnConfig.TLSConfig = tlsConfigs[0]
	config.ConnConfig.Fallbacks = fallbackConfigs(settings.host, settings.port, tlsConfigs)
	config.ConnConfig.RuntimeParams = make(map[string]string)

	return config, nil
}

type runtimeSettings struct {
	username string
	password string
	host     string
	port     uint16
	database string
	sslMode  string
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
		parsed, err := strconv.ParseUint(defaultPort, 10, 16)
		if err != nil || parsed == 0 {
			return 0, errInvalidRuntimeDefinition
		}

		return uint16(parsed), nil
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

func tlsConfigs(host, sslMode string) ([]*tls.Config, error) {
	switch sslMode {
	case sslModeDisable:
		return []*tls.Config{nil}, nil
	case sslModeAllow:
		return []*tls.Config{nil, insecureTLSConfig(host)}, nil
	case sslModePrefer:
		return []*tls.Config{insecureTLSConfig(host), nil}, nil
	case sslModeRequire:
		return []*tls.Config{insecureTLSConfig(host)}, nil
	case sslModeVerifyCA:
		config, err := verifyCAConfig(host)
		return []*tls.Config{config}, err
	case sslModeVerifyFull:
		config, err := verifyFullConfig(host)
		return []*tls.Config{config}, err
	default:
		return nil, errInvalidRuntimeDefinition
	}
}

func insecureTLSConfig(host string) *tls.Config {
	return &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true, //nolint:gosec // PostgreSQL allow, prefer, and require modes intentionally permit this behavior.
	}
}

func verifyCAConfig(host string) (*tls.Config, error) {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		return nil, errInvalidRuntimeDefinition
	}

	return &tls.Config{
		ServerName:         host,
		RootCAs:            roots,
		InsecureSkipVerify: true, //nolint:gosec // Certificate-chain verification is performed by VerifyConnection without hostname verification.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errInvalidRuntimeDefinition
			}

			intermediates := x509.NewCertPool()
			for _, certificate := range state.PeerCertificates[1:] {
				intermediates.AddCert(certificate)
			}

			_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots:         roots,
				Intermediates: intermediates,
			})
			if err != nil {
				return errInvalidRuntimeDefinition
			}

			return nil
		},
	}, nil
}

func verifyFullConfig(host string) (*tls.Config, error) {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		return nil, errInvalidRuntimeDefinition
	}

	return &tls.Config{
		ServerName: host,
		RootCAs:    roots,
	}, nil
}

func fallbackConfigs(host string, port uint16, configs []*tls.Config) []*pgconn.FallbackConfig {
	if len(configs) <= 1 {
		return nil
	}

	fallbacks := make([]*pgconn.FallbackConfig, 0, len(configs)-1)
	for _, config := range configs[1:] {
		fallbacks = append(fallbacks, &pgconn.FallbackConfig{
			Host:      host,
			Port:      port,
			TLSConfig: config,
		})
	}

	return fallbacks
}
