package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

const (
	defaultHTTPAddress          = "127.0.0.1:8080"
	defaultResourceLimit        = 100
	maxResourceLimit            = 1000
	defaultAdminSocketPath      = "/run/dataporch/admin.sock"
	defaultMasterKeyPath        = "/etc/dataporch/master.key"
	defaultSecretsStorePath     = "/var/lib/dataporch/secrets.store" //nolint:gosec // This is a path, not a credential.
	defaultConnectionsStorePath = "/var/lib/dataporch/connections.store"
)

var errLookupRequired = errors.New("config: environment lookup is required")

type LookupEnv func(string) (string, bool)

// Config errors retain uppercase environment variable names so diagnostics match operator-facing keys.
type Config struct {
	HTTPAddress          string
	ResourceLimit        int
	ShutdownPeriod       time.Duration
	AdminSocketPath      string
	MasterKeyPath        string
	SecretsStorePath     string
	ConnectionsStorePath string
}

func Load(lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, errLookupRequired
	}

	cfg := Config{
		HTTPAddress:          defaultHTTPAddress,
		ResourceLimit:        defaultResourceLimit,
		ShutdownPeriod:       10 * time.Second,
		AdminSocketPath:      defaultAdminSocketPath,
		MasterKeyPath:        defaultMasterKeyPath,
		SecretsStorePath:     defaultSecretsStorePath,
		ConnectionsStorePath: defaultConnectionsStorePath,
	}

	if value, exists := lookup("DATAPORCH_HTTP_ADDRESS"); exists {
		cfg.HTTPAddress = value
	}

	if value, exists := lookup("DATAPORCH_RESOURCE_LIMIT"); exists {
		limit, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parsing DATAPORCH_RESOURCE_LIMIT: %w", err)
		}

		cfg.ResourceLimit = limit
	}

	if value, exists := lookup("DATAPORCH_ADMIN_SOCKET_PATH"); exists {
		cfg.AdminSocketPath = value
	}

	if value, exists := lookup("DATAPORCH_MASTER_KEY_PATH"); exists {
		cfg.MasterKeyPath = value
	}

	if value, exists := lookup("DATAPORCH_SECRETS_STORE_PATH"); exists {
		cfg.SecretsStorePath = value
	}

	if value, exists := lookup("DATAPORCH_CONNECTIONS_STORE_PATH"); exists {
		cfg.ConnectionsStorePath = value
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.HTTPAddress); err != nil {
		return fmt.Errorf("validating DATAPORCH_HTTP_ADDRESS: %w", err)
	}

	if c.ResourceLimit <= 0 || c.ResourceLimit > maxResourceLimit {
		return fmt.Errorf(
			"validating DATAPORCH_RESOURCE_LIMIT: must be between 1 and %d",
			maxResourceLimit,
		)
	}

	if c.ShutdownPeriod <= 0 {
		return errors.New("validating shutdown period: must be positive")
	}

	if c.AdminSocketPath == "" {
		return errors.New("validating DATAPORCH_ADMIN_SOCKET_PATH: must not be empty")
	}

	if c.MasterKeyPath == "" {
		return errors.New("validating DATAPORCH_MASTER_KEY_PATH: must not be empty")
	}

	if c.SecretsStorePath == "" {
		return errors.New("validating DATAPORCH_SECRETS_STORE_PATH: must not be empty")
	}

	if c.ConnectionsStorePath == "" {
		return errors.New("validating DATAPORCH_CONNECTIONS_STORE_PATH: must not be empty")
	}

	if c.MasterKeyPath == c.SecretsStorePath {
		return errors.New("validating security paths: master key and secrets store must differ")
	}

	if c.SecretsStorePath == c.ConnectionsStorePath {
		return errors.New("validating security paths: secrets and connections stores must differ")
	}

	return nil
}
