package config

import "testing"

func TestLoadSecurityPaths(t *testing.T) {
	t.Parallel()

	const (
		wantAdminSocket     = "/run/dataporch/admin.sock"
		wantMasterKey       = "/etc/dataporch/master.key"
		wantSecretsStore    = "/var/lib/dataporch/secrets.store"
		wantConnectionsFile = "/var/lib/dataporch/connections.store"
	)

	tests := []struct {
		name                string
		values              map[string]string
		wantAdminSocket     string
		wantMasterKey       string
		wantSecretsStore    string
		wantConnectionsFile string
	}{
		{
			name:                "defaults",
			values:              map[string]string{},
			wantAdminSocket:     wantAdminSocket,
			wantMasterKey:       wantMasterKey,
			wantSecretsStore:    wantSecretsStore,
			wantConnectionsFile: wantConnectionsFile,
		},
		{
			name: "overrides",
			values: map[string]string{
				"DATAPORCH_ADMIN_SOCKET_PATH":      "/tmp/dataporch/admin.sock",
				"DATAPORCH_MASTER_KEY_PATH":        "/tmp/dataporch/master.key",
				"DATAPORCH_SECRETS_STORE_PATH":     "/tmp/dataporch/secrets.store",
				"DATAPORCH_CONNECTIONS_STORE_PATH": "/tmp/dataporch/connections.store",
			},
			wantAdminSocket:     "/tmp/dataporch/admin.sock",
			wantMasterKey:       "/tmp/dataporch/master.key",
			wantSecretsStore:    "/tmp/dataporch/secrets.store",
			wantConnectionsFile: "/tmp/dataporch/connections.store",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Load(func(key string) (string, bool) {
				value, exists := tt.values[key]
				return value, exists
			})
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
		})
	}
}

func TestValidateRejectsInvalidSecurityPaths(t *testing.T) {
	t.Parallel()

	valid := Config{
		HTTPAddress:          "127.0.0.1:8080",
		ResourceLimit:        1,
		ShutdownPeriod:       1,
		AdminSocketPath:      "/run/dataporch/admin.sock",
		MasterKeyPath:        "/etc/dataporch/master.key",
		SecretsStorePath:     "/var/lib/dataporch/secrets.store",
		ConnectionsStorePath: "/var/lib/dataporch/connections.store",
	}

	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "empty admin socket", change: func(cfg *Config) { cfg.AdminSocketPath = "" }},
		{name: "empty master key", change: func(cfg *Config) { cfg.MasterKeyPath = "" }},
		{name: "empty secrets store", change: func(cfg *Config) { cfg.SecretsStorePath = "" }},
		{name: "empty connections store", change: func(cfg *Config) { cfg.ConnectionsStorePath = "" }},
		{name: "key equals secret store", change: func(cfg *Config) { cfg.SecretsStorePath = cfg.MasterKeyPath }},
		{
			name: "secret equals connection store",
			change: func(cfg *Config) {
				cfg.ConnectionsStorePath = cfg.SecretsStorePath
			},
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

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		values    map[string]string
		wantAddr  string
		wantLimit int
		wantError bool
	}{
		{
			name:      "defaults",
			values:    map[string]string{},
			wantAddr:  defaultHTTPAddress,
			wantLimit: defaultResourceLimit,
		},
		{
			name: "overrides",
			values: map[string]string{
				"DATAPORCH_HTTP_ADDRESS":   "127.0.0.1:9090",
				"DATAPORCH_RESOURCE_LIMIT": "25",
			},
			wantAddr:  "127.0.0.1:9090",
			wantLimit: 25,
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
		})
	}
}

func TestLoadRejectsMissingLookup(t *testing.T) {
	t.Parallel()

	if _, err := Load(nil); err == nil {
		t.Fatal("Load(nil) error = nil, want non-nil")
	}
}
