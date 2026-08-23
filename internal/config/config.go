package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	defaultHTTPAddress            = "127.0.0.1:8080"
	defaultResourceLimit          = 100
	maxResourceLimit              = 1000
	mcpTokenStorePathEnv          = "DATAPORCH_MCP_TOKEN_STORE_PATH" //nolint:gosec // This is an environment variable name, not a credential.
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
type UserHomeDir func() (string, error)

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
func Load(lookup LookupEnv, homes ...UserHomeDir) (Config, error) {
	if lookup == nil {
		return Config{}, errLookupRequired
	}
	home := os.UserHomeDir
	if len(homes) > 1 {
		return Config{}, errors.New("config: too many home directory resolvers")
	}
	if len(homes) == 1 {
		if homes[0] == nil {
			return Config{}, errors.New("config: home directory resolver is required")
		}
		home = homes[0]
	}

	cfg := Config{
		HTTPAddress:            defaultHTTPAddress,
		ResourceLimit:          defaultResourceLimit,
		ShutdownPeriod:         10 * time.Second,
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

	if value, exists := lookup(mcpTokenStorePathEnv); exists {
		if value == "" {
			return Config{}, fmt.Errorf("validating %s: must not be empty", mcpTokenStorePathEnv)
		}
		cfg.MCPTokenStorePath = value
	}

	if cfg.AdminSocketPath == "" || cfg.MasterKeyPath == "" || cfg.SecretsStorePath == "" || cfg.ConnectionsStorePath == "" || cfg.MCPTokenStorePath == "" {
		resolvedHome, err := home()
		if err != nil {
			return Config{}, fmt.Errorf("resolving user home directory: %w", err)
		}
		if resolvedHome == "" || !filepath.IsAbs(resolvedHome) {
			return Config{}, errors.New("resolving user home directory: must be an absolute path")
		}
		base := filepath.Join(resolvedHome, ".dataporch")
		if cfg.AdminSocketPath == "" {
			cfg.AdminSocketPath = filepath.Join(base, "admin.sock")
		}
		if cfg.MasterKeyPath == "" {
			cfg.MasterKeyPath = filepath.Join(base, "master.key")
		}
		if cfg.SecretsStorePath == "" {
			cfg.SecretsStorePath = filepath.Join(base, "secrets.store")
		}
		if cfg.ConnectionsStorePath == "" {
			cfg.ConnectionsStorePath = filepath.Join(base, "connections.store")
		}
		if cfg.MCPTokenStorePath == "" {
			cfg.MCPTokenStorePath = filepath.Join(base, "mcp-token.json")
		}
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
		return fmt.Errorf("validating %s: must not be empty", mcpTokenStorePathEnv)
	}

	for _, path := range []struct {
		name  string
		value string
	}{
		{name: "DATAPORCH_ADMIN_SOCKET_PATH", value: c.AdminSocketPath},
		{name: "DATAPORCH_MASTER_KEY_PATH", value: c.MasterKeyPath},
		{name: "DATAPORCH_SECRETS_STORE_PATH", value: c.SecretsStorePath},
		{name: "DATAPORCH_CONNECTIONS_STORE_PATH", value: c.ConnectionsStorePath},
		{name: mcpTokenStorePathEnv, value: c.MCPTokenStorePath},
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
