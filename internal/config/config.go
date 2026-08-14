package config

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"time"
)

const (
	defaultHTTPAddress            = "127.0.0.1:8080"
	defaultResourceLimit          = 100
	maxResourceLimit              = 1000
	defaultAdminSocketPath        = "/run/dataporch/admin.sock"
	defaultMasterKeyPath          = "/etc/dataporch/master.key"
	defaultSecretsStorePath       = "/var/lib/dataporch/secrets.store" //nolint:gosec // This is a path, not a credential.
	defaultConnectionsStorePath   = "/var/lib/dataporch/connections.store"
	defaultMCPTokenStorePath      = "/var/lib/dataporch/mcp-token.json" //nolint:gosec // This is a path, not a credential.
	defaultQueryTimeout           = 20 * time.Second
	minQueryTimeout               = time.Second
	maxQueryTimeout               = 20 * time.Second
	defaultQueryResponseByteLimit = 10_485_760
	minQueryResponseByteLimit     = 65_536
	maxQueryResponseByteLimit     = 10_485_760
	defaultQueryTruncationEnabled = true
	defaultQueryRowLimit          = 1000
)

var errLookupRequired = errors.New("config: environment lookup is required")

type LookupEnv func(string) (string, bool)

// Config errors retain uppercase environment variable names so diagnostics match operator-facing keys.
type Config struct {
	HTTPAddress            string
	ResourceLimit          int
	ShutdownPeriod         time.Duration
	AdminSocketPath        string
	MasterKeyPath          string
	SecretsStorePath       string
	ConnectionsStorePath   string
	MCPTokenStorePath      string
	QueryTimeout           time.Duration
	QueryResponseByteLimit int
	QueryTruncationEnabled bool
	QueryRowLimit          int
}

//nolint:gocyclo // Explicit environment parsing preserves operator-facing keys and error context.
func Load(lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, errLookupRequired
	}

	cfg := Config{
		HTTPAddress:            defaultHTTPAddress,
		ResourceLimit:          defaultResourceLimit,
		ShutdownPeriod:         10 * time.Second,
		AdminSocketPath:        defaultAdminSocketPath,
		MasterKeyPath:          defaultMasterKeyPath,
		SecretsStorePath:       defaultSecretsStorePath,
		ConnectionsStorePath:   defaultConnectionsStorePath,
		MCPTokenStorePath:      defaultMCPTokenStorePath,
		QueryTimeout:           defaultQueryTimeout,
		QueryResponseByteLimit: defaultQueryResponseByteLimit,
		QueryTruncationEnabled: defaultQueryTruncationEnabled,
		QueryRowLimit:          defaultQueryRowLimit,
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

	if value, exists := lookup("DATAPORCH_MCP_TOKEN_STORE_PATH"); exists {
		cfg.MCPTokenStorePath = value
	}

	if value, exists := lookup("DATAPORCH_QUERY_TIMEOUT"); exists {
		timeout, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("parsing DATAPORCH_QUERY_TIMEOUT: %w", err)
		}

		cfg.QueryTimeout = timeout
	}

	if value, exists := lookup("DATAPORCH_QUERY_RESPONSE_BYTE_LIMIT"); exists {
		limit, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parsing DATAPORCH_QUERY_RESPONSE_BYTE_LIMIT: %w", err)
		}

		cfg.QueryResponseByteLimit = limit
	}

	if value, exists := lookup("DATAPORCH_QUERY_TRUNCATION_ENABLED"); exists {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("parsing DATAPORCH_QUERY_TRUNCATION_ENABLED: %w", err)
		}

		cfg.QueryTruncationEnabled = enabled
	}

	if value, exists := lookup("DATAPORCH_QUERY_ROW_LIMIT"); exists {
		limit, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parsing DATAPORCH_QUERY_ROW_LIMIT: %w", err)
		}

		cfg.QueryRowLimit = limit
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

//nolint:gocyclo // Each independent configuration invariant has a distinct operator-facing diagnostic.
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

	if c.MCPTokenStorePath == "" {
		return errors.New("validating DATAPORCH_MCP_TOKEN_STORE_PATH: must not be empty")
	}

	for _, path := range []struct {
		name  string
		value string
	}{
		{name: "DATAPORCH_ADMIN_SOCKET_PATH", value: c.AdminSocketPath},
		{name: "DATAPORCH_MASTER_KEY_PATH", value: c.MasterKeyPath},
		{name: "DATAPORCH_SECRETS_STORE_PATH", value: c.SecretsStorePath},
		{name: "DATAPORCH_CONNECTIONS_STORE_PATH", value: c.ConnectionsStorePath},
		{name: "DATAPORCH_MCP_TOKEN_STORE_PATH", value: c.MCPTokenStorePath},
	} {
		if !filepath.IsAbs(path.value) {
			return fmt.Errorf("validating %s: must be absolute", path.name)
		}
	}

	if c.QueryTimeout < minQueryTimeout || c.QueryTimeout > maxQueryTimeout {
		return fmt.Errorf(
			"validating DATAPORCH_QUERY_TIMEOUT: must be between %s and %s",
			minQueryTimeout,
			maxQueryTimeout,
		)
	}

	if c.QueryResponseByteLimit < minQueryResponseByteLimit ||
		c.QueryResponseByteLimit > maxQueryResponseByteLimit {
		return fmt.Errorf(
			"validating DATAPORCH_QUERY_RESPONSE_BYTE_LIMIT: must be between %d and %d",
			minQueryResponseByteLimit,
			maxQueryResponseByteLimit,
		)
	}

	if c.QueryTruncationEnabled && c.QueryRowLimit <= 0 {
		return errors.New(
			"validating DATAPORCH_QUERY_ROW_LIMIT: must be positive when truncation is enabled",
		)
	}

	paths := []struct {
		name string
		path string
	}{
		{name: "admin socket", path: filepath.Clean(c.AdminSocketPath)},
		{name: "master key", path: filepath.Clean(c.MasterKeyPath)},
		{name: "secrets store", path: filepath.Clean(c.SecretsStorePath)},
		{name: "connections store", path: filepath.Clean(c.ConnectionsStorePath)},
		{name: "MCP token store", path: filepath.Clean(c.MCPTokenStorePath)},
	}

	seen := make(map[string]string, len(paths))
	for _, current := range paths {
		if previous, exists := seen[current.path]; exists {
			return fmt.Errorf(
				"validating security paths: %s must differ from %s",
				current.name,
				previous,
			)
		}

		seen[current.path] = current.name
	}

	return nil
}
