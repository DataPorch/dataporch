package config

import (
	"maps"
	"strings"
	"testing"
	"time"
)

func TestLoadSecurityPaths(t *testing.T) {
	t.Parallel()

	const (
		wantAdminSocket     = "/Users/alice/.dataporch/admin.sock"
		wantMasterKey       = "/Users/alice/.dataporch/master.key"
		wantSecretsStore    = "/Users/alice/.dataporch/secrets.store"
		wantConnectionsFile = "/Users/alice/.dataporch/connections.store"
	)

	tests := []struct {
		name                string
		values              map[string]string
		wantAdminSocket     string
		wantMasterKey       string
		wantSecretsStore    string
		wantConnectionsFile string
		wantTokenStore      string
	}{
		{
			name:                "defaults",
			values:              map[string]string{},
			wantAdminSocket:     wantAdminSocket,
			wantMasterKey:       wantMasterKey,
			wantSecretsStore:    wantSecretsStore,
			wantConnectionsFile: wantConnectionsFile,
			wantTokenStore:      "/Users/alice/.dataporch/mcp-token.json",
		},
		{
			name: "overrides",
			values: map[string]string{
				"DATAPORCH_ADMIN_SOCKET_PATH":      "/tmp/dataporch/admin.sock",
				"DATAPORCH_MASTER_KEY_PATH":        "/tmp/dataporch/master.key",
				"DATAPORCH_SECRETS_STORE_PATH":     "/tmp/dataporch/secrets.store",
				"DATAPORCH_CONNECTIONS_STORE_PATH": "/tmp/dataporch/connections.store",
				mcpTokenStorePathEnv:               "/tmp/dataporch/mcp-token.json",
			},
			wantAdminSocket:     "/tmp/dataporch/admin.sock",
			wantMasterKey:       "/tmp/dataporch/master.key",
			wantSecretsStore:    "/tmp/dataporch/secrets.store",
			wantConnectionsFile: "/tmp/dataporch/connections.store",
			wantTokenStore:      "/tmp/dataporch/mcp-token.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Load(func(key string) (string, bool) {
				value, exists := tt.values[key]
				return value, exists
			}, func() (string, error) { return "/Users/alice", nil })
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			if cfg.AdminSocketPath != tt.wantAdminSocket {
				t.Errorf("AdminSocketPath = %q, want %q", cfg.AdminSocketPath, tt.wantAdminSocket)
			}

			if cfg.MasterKeyPath != tt.wantMasterKey {
				t.Errorf("MasterKeyPath = %q, want %q", cfg.MasterKeyPath, tt.wantMasterKey)
			}

			if cfg.SecretsStorePath != tt.wantSecretsStore {
				t.Errorf("SecretsStorePath = %q, want %q", cfg.SecretsStorePath, tt.wantSecretsStore)
			}

			if cfg.ConnectionsStorePath != tt.wantConnectionsFile {
				t.Errorf("ConnectionsStorePath = %q, want %q", cfg.ConnectionsStorePath, tt.wantConnectionsFile)
			}

			if cfg.MCPTokenStorePath != tt.wantTokenStore {
				t.Errorf("MCPTokenStorePath = %q, want %q", cfg.MCPTokenStorePath, tt.wantTokenStore)
			}
		})
	}
}

func TestValidateRejectsInvalidSecurityPaths(t *testing.T) {
	t.Parallel()

	valid := Config{
		HTTPAddress:            "127.0.0.1:8080",
		ResourceLimit:          1,
		ShutdownPeriod:         1,
		AdminSocketPath:        "/run/dataporch/admin.sock",
		MasterKeyPath:          "/etc/dataporch/master.key",
		SecretsStorePath:       "/var/lib/dataporch/secrets.store",
		ConnectionsStorePath:   "/var/lib/dataporch/connections.store",
		MCPTokenStorePath:      "/var/lib/dataporch/mcp-token.json",
		QueryTimeout:           20 * time.Second,
		QueryResponseByteLimit: 10_485_760,
		QueryTruncationEnabled: true,
		QueryRowLimit:          1000,
	}

	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "empty admin socket", change: func(cfg *Config) { cfg.AdminSocketPath = "" }},
		{name: "empty master key", change: func(cfg *Config) { cfg.MasterKeyPath = "" }},
		{name: "empty secrets store", change: func(cfg *Config) { cfg.SecretsStorePath = "" }},
		{name: "empty connections store", change: func(cfg *Config) { cfg.ConnectionsStorePath = "" }},
		{name: "empty token store", change: func(cfg *Config) { cfg.MCPTokenStorePath = "" }},
		{name: "relative admin socket", change: func(cfg *Config) { cfg.AdminSocketPath = "run/dataporch/admin.sock" }},
		{name: "relative master key", change: func(cfg *Config) { cfg.MasterKeyPath = "etc/dataporch/master.key" }},
		{name: "relative secrets store", change: func(cfg *Config) { cfg.SecretsStorePath = "var/lib/dataporch/secrets.store" }},
		{name: "relative connections store", change: func(cfg *Config) { cfg.ConnectionsStorePath = "var/lib/dataporch/connections.store" }},
		{name: "relative token store", change: func(cfg *Config) { cfg.MCPTokenStorePath = "var/lib/dataporch/mcp-token.json" }},
		{name: "key equals secret store", change: func(cfg *Config) { cfg.SecretsStorePath = cfg.MasterKeyPath }},
		{
			name: "secret equals connection store",
			change: func(cfg *Config) {
				cfg.ConnectionsStorePath = cfg.SecretsStorePath
			},
		},
		{name: "token store equals master key", change: func(cfg *Config) { cfg.MCPTokenStorePath = cfg.MasterKeyPath }},
		{name: "token store equals secret store", change: func(cfg *Config) { cfg.MCPTokenStorePath = cfg.SecretsStorePath }},
		{name: "token store equals connection store", change: func(cfg *Config) { cfg.MCPTokenStorePath = cfg.ConnectionsStorePath }},
		{name: "token store equals admin socket", change: func(cfg *Config) { cfg.MCPTokenStorePath = cfg.AdminSocketPath }},
		{
			name:   "admin socket aliases master key",
			change: func(cfg *Config) { cfg.MasterKeyPath = "/run/dataporch/./admin.sock" },
		},
		{
			name:   "admin socket aliases secrets store",
			change: func(cfg *Config) { cfg.SecretsStorePath = "/run/dataporch/./admin.sock" },
		},
		{
			name:   "admin socket aliases connections store",
			change: func(cfg *Config) { cfg.ConnectionsStorePath = "/run/dataporch/tmp/../admin.sock" },
		},
		{
			name:   "master key aliases connections store",
			change: func(cfg *Config) { cfg.MasterKeyPath = "/var/lib/dataporch/tmp/../connections.store" },
		},
		{
			name:   "token store aliases master key",
			change: func(cfg *Config) { cfg.MCPTokenStorePath = "/etc/dataporch/./master.key" },
		},
		{
			name:   "token store aliases secrets store",
			change: func(cfg *Config) { cfg.MCPTokenStorePath = "/var/lib/dataporch/./secrets.store" },
		},
		{
			name:   "token store aliases connections store",
			change: func(cfg *Config) { cfg.MCPTokenStorePath = "/var/lib/dataporch/tmp/../connections.store" },
		},
		{
			name:   "token store aliases admin socket",
			change: func(cfg *Config) { cfg.MCPTokenStorePath = "/run/dataporch/tmp/../admin.sock" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := valid
			tt.change(&cfg)

			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want non-nil")
			}
		})
	}
}

func TestLoadRejectsInvalidMCPTokenStorePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		values        map[string]string
		wantErrorPart string
	}{
		{
			name:          "empty path",
			values:        map[string]string{mcpTokenStorePathEnv: ""},
			wantErrorPart: mcpTokenStorePathEnv + ": must not be empty",
		},
		{
			name: "master key collision",
			values: map[string]string{
				mcpTokenStorePathEnv: "/etc/dataporch/master.key",
			},
			wantErrorPart: "MCP token store must differ from",
		},
		{
			name: "encrypted connector secret store collision",
			values: map[string]string{
				mcpTokenStorePathEnv: "/var/lib/dataporch/secrets.store",
			},
			wantErrorPart: "MCP token store must differ from",
		},
		{
			name: "connection definition store collision",
			values: map[string]string{
				mcpTokenStorePathEnv: "/var/lib/dataporch/connections.store",
			},
			wantErrorPart: "MCP token store must differ from",
		},
		{
			name: "admin socket collision",
			values: map[string]string{
				mcpTokenStorePathEnv: "/run/dataporch/./admin.sock",
			},
			wantErrorPart: "MCP token store must differ from",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			values := map[string]string{
				"DATAPORCH_ADMIN_SOCKET_PATH":      "/run/dataporch/admin.sock",
				"DATAPORCH_MASTER_KEY_PATH":        "/etc/dataporch/master.key",
				"DATAPORCH_SECRETS_STORE_PATH":     "/var/lib/dataporch/secrets.store",
				"DATAPORCH_CONNECTIONS_STORE_PATH": "/var/lib/dataporch/connections.store",
			}
			maps.Copy(values, tt.values)
			_, err := Load(func(key string) (string, bool) {
				value, exists := values[key]
				return value, exists
			}, func() (string, error) { return "/Users/alice", nil })
			if err == nil {
				t.Fatal("Load() error = nil, want non-nil")
			}

			if !strings.Contains(err.Error(), tt.wantErrorPart) {
				t.Fatalf("Load() error = %q, want %q", err, tt.wantErrorPart)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                       string
		values                     map[string]string
		wantAddr                   string
		wantLimit                  int
		wantQueryTimeout           time.Duration
		wantQueryResponseByteLimit int
		wantQueryTruncationEnabled bool
		wantQueryRowLimit          int
		wantError                  bool
	}{
		{
			name:                       "defaults",
			values:                     map[string]string{},
			wantAddr:                   defaultHTTPAddress,
			wantLimit:                  defaultResourceLimit,
			wantQueryTimeout:           20 * time.Second,
			wantQueryResponseByteLimit: 10_485_760,
			wantQueryTruncationEnabled: true,
			wantQueryRowLimit:          1000,
		},
		{
			name: "overrides",
			values: map[string]string{
				"DATAPORCH_HTTP_ADDRESS":              "127.0.0.1:9090",
				"DATAPORCH_RESOURCE_LIMIT":            "25",
				"DATAPORCH_QUERY_TIMEOUT":             "3s",
				"DATAPORCH_QUERY_RESPONSE_BYTE_LIMIT": "65536",
				"DATAPORCH_QUERY_TRUNCATION_ENABLED":  "false",
				"DATAPORCH_QUERY_ROW_LIMIT":           "0",
			},
			wantAddr:                   "127.0.0.1:9090",
			wantLimit:                  25,
			wantQueryTimeout:           3 * time.Second,
			wantQueryResponseByteLimit: 65_536,
			wantQueryTruncationEnabled: false,
			wantQueryRowLimit:          0,
		},
		{
			name: "invalid address",
			values: map[string]string{
				"DATAPORCH_HTTP_ADDRESS": "localhost",
			},
			wantError: true,
		},
		{
			name: "invalid limit",
			values: map[string]string{
				"DATAPORCH_RESOURCE_LIMIT": "0",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lookup := func(key string) (string, bool) {
				value, exists := tt.values[key]
				return value, exists
			}

			cfg, err := Load(lookup)
			if (err != nil) != tt.wantError {
				t.Fatalf("Load() error = %v, wantError %v", err, tt.wantError)
			}

			if tt.wantError {
				return
			}

			if cfg.HTTPAddress != tt.wantAddr {
				t.Errorf("HTTPAddress = %q, want %q", cfg.HTTPAddress, tt.wantAddr)
			}

			if cfg.ResourceLimit != tt.wantLimit {
				t.Errorf("ResourceLimit = %d, want %d", cfg.ResourceLimit, tt.wantLimit)
			}

			if cfg.QueryTimeout != tt.wantQueryTimeout {
				t.Errorf("QueryTimeout = %s, want %s", cfg.QueryTimeout, tt.wantQueryTimeout)
			}

			if cfg.QueryResponseByteLimit != tt.wantQueryResponseByteLimit {
				t.Errorf("QueryResponseByteLimit = %d, want %d", cfg.QueryResponseByteLimit, tt.wantQueryResponseByteLimit)
			}

			if cfg.QueryTruncationEnabled != tt.wantQueryTruncationEnabled {
				t.Errorf("QueryTruncationEnabled = %t, want %t", cfg.QueryTruncationEnabled, tt.wantQueryTruncationEnabled)
			}

			if cfg.QueryRowLimit != tt.wantQueryRowLimit {
				t.Errorf("QueryRowLimit = %d, want %d", cfg.QueryRowLimit, tt.wantQueryRowLimit)
			}
		})
	}
}

func TestLoadRejectsInvalidQuerySettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "timeout malformed", values: map[string]string{"DATAPORCH_QUERY_TIMEOUT": "soon"}},
		{name: "timeout below minimum", values: map[string]string{"DATAPORCH_QUERY_TIMEOUT": "999ms"}},
		{name: "timeout above maximum", values: map[string]string{"DATAPORCH_QUERY_TIMEOUT": "20s1ms"}},
		{name: "response malformed", values: map[string]string{"DATAPORCH_QUERY_RESPONSE_BYTE_LIMIT": "ten-megabytes"}},
		{name: "response below minimum", values: map[string]string{"DATAPORCH_QUERY_RESPONSE_BYTE_LIMIT": "65535"}},
		{name: "response above maximum", values: map[string]string{"DATAPORCH_QUERY_RESPONSE_BYTE_LIMIT": "10485761"}},
		{name: "truncation malformed", values: map[string]string{"DATAPORCH_QUERY_TRUNCATION_ENABLED": "sometimes"}},
		{name: "row limit malformed", values: map[string]string{"DATAPORCH_QUERY_ROW_LIMIT": "many"}},
		{name: "enabled row limit zero", values: map[string]string{"DATAPORCH_QUERY_ROW_LIMIT": "0"}},
		{name: "enabled row limit negative", values: map[string]string{"DATAPORCH_QUERY_ROW_LIMIT": "-1"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(func(key string) (string, bool) {
				value, exists := test.values[key]
				return value, exists
			})
			if err == nil {
				t.Fatal("Load() error = nil, want non-nil")
			}
		})
	}
}

func TestValidateQueryBounds(t *testing.T) {
	t.Parallel()

	base := Config{
		HTTPAddress:            "127.0.0.1:8080",
		ResourceLimit:          1,
		ShutdownPeriod:         time.Second,
		AdminSocketPath:        "/run/dataporch/admin.sock",
		MasterKeyPath:          "/etc/dataporch/master.key",
		SecretsStorePath:       "/var/lib/dataporch/secrets.store",
		ConnectionsStorePath:   "/var/lib/dataporch/connections.store",
		MCPTokenStorePath:      "/var/lib/dataporch/mcp-token.json",
		QueryTimeout:           time.Second,
		QueryResponseByteLimit: 65_536,
		QueryTruncationEnabled: true,
		QueryRowLimit:          1,
	}

	valid := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "minimums", mutate: func(*Config) {}},
		{name: "maximums", mutate: func(cfg *Config) {
			cfg.QueryTimeout = 20 * time.Second
			cfg.QueryResponseByteLimit = 10_485_760
		}},
		{name: "disabled truncation allows zero rows", mutate: func(cfg *Config) {
			cfg.QueryTruncationEnabled = false
			cfg.QueryRowLimit = 0
		}},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cfg := base
			test.mutate(&cfg)

			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}

	invalid := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "timeout below minimum", mutate: func(cfg *Config) {
			cfg.QueryTimeout = time.Second - time.Nanosecond
		}},
		{name: "timeout above maximum", mutate: func(cfg *Config) {
			cfg.QueryTimeout = 20*time.Second + time.Nanosecond
		}},
		{name: "response below minimum", mutate: func(cfg *Config) {
			cfg.QueryResponseByteLimit = 65_535
		}},
		{name: "response above maximum", mutate: func(cfg *Config) {
			cfg.QueryResponseByteLimit = 10_485_761
		}},
		{name: "enabled zero rows", mutate: func(cfg *Config) {
			cfg.QueryRowLimit = 0
		}},
		{name: "enabled negative rows", mutate: func(cfg *Config) {
			cfg.QueryRowLimit = -1
		}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cfg := base
			test.mutate(&cfg)

			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want non-nil")
			}
		})
	}
}

func TestLoadRejectsMissingLookup(t *testing.T) {
	t.Parallel()

	if _, err := Load(nil); err == nil {
		t.Fatal("Load(nil) error = nil, want non-nil")
	}
}
