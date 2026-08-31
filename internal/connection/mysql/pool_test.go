package mysql

import (
	"errors"
	"testing"
	"time"

	"github.com/DataPorch/dataporch/internal/connection"
)

func validRuntimeDefinition() connection.ResolvedDefinition {
	return connection.ResolvedDefinition{
		ID:   "finance",
		Kind: Kind,
		Settings: map[string]string{
			settingUsername: "reader",
			settingHost:     "127.0.0.1",
			settingDatabase: "finance",
		},
		Secrets: map[string][]byte{
			settingPassword: []byte("secret"),
		},
	}
}

func TestValidateRuntimeDefinitionDefaults(t *testing.T) {
	t.Parallel()

	settings, err := validateRuntimeDefinition(validRuntimeDefinition())
	if err != nil {
		t.Fatalf("validateRuntimeDefinition() error = %v", err)
	}

	if settings.port != 3306 {
		t.Fatalf("port = %d, want 3306", settings.port)
	}

	if settings.sslMode != sslModePrefer {
		t.Fatalf("sslMode = %q, want %q", settings.sslMode, sslModePrefer)
	}
}

func TestValidateRuntimeDefinitionRejectsInvalidDefinitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*connection.ResolvedDefinition)
	}{
		{name: "empty id", mutate: func(def *connection.ResolvedDefinition) { def.ID = "" }},
		{name: "wrong kind", mutate: func(def *connection.ResolvedDefinition) { def.Kind = "postgres" }},
		{
			name: "missing username",
			mutate: func(def *connection.ResolvedDefinition) {
				delete(def.Settings, settingUsername)
			},
		},
		{
			name: "missing host",
			mutate: func(def *connection.ResolvedDefinition) {
				delete(def.Settings, settingHost)
			},
		},
		{
			name: "missing database",
			mutate: func(def *connection.ResolvedDefinition) {
				delete(def.Settings, settingDatabase)
			},
		},
		{
			name: "missing password",
			mutate: func(def *connection.ResolvedDefinition) {
				delete(def.Secrets, settingPassword)
			},
		},
		{
			name: "unknown setting",
			mutate: func(def *connection.ResolvedDefinition) {
				def.Settings["charset"] = "utf8mb4"
			},
		},
		{
			name: "extra secret",
			mutate: func(def *connection.ResolvedDefinition) {
				def.Secrets["token"] = []byte("x")
			},
		},
		{
			name: "empty username",
			mutate: func(def *connection.ResolvedDefinition) {
				def.Settings[settingUsername] = ""
			},
		},
		{
			name: "nul host",
			mutate: func(def *connection.ResolvedDefinition) {
				def.Settings[settingHost] = "db\x00.example.com"
			},
		},
		{
			name: "zero port",
			mutate: func(def *connection.ResolvedDefinition) {
				def.Settings[settingPort] = "0"
			},
		},
		{
			name: "invalid port",
			mutate: func(def *connection.ResolvedDefinition) {
				def.Settings[settingPort] = "notaport"
			},
		},
		{
			name: "invalid sslmode",
			mutate: func(def *connection.ResolvedDefinition) {
				def.Settings[settingSSLMode] = "verify-ca"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			definition := validRuntimeDefinition()
			test.mutate(&definition)

			_, err := validateRuntimeDefinition(definition)
			if !errors.Is(err, errInvalidRuntimeDefinition) {
				t.Fatalf("validateRuntimeDefinition() error = %v, want %v", err, errInvalidRuntimeDefinition)
			}
		})
	}
}

//nolint:gocyclo // Each TLS mode is asserted with its independent security contract.
func TestDriverConfigTLSModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sslMode      string
		wantTLS      bool
		wantFallback bool
		wantInsecure bool
		wantRoots    bool
	}{
		{name: "disable", sslMode: sslModeDisable},
		{
			name:         "prefer",
			sslMode:      sslModePrefer,
			wantTLS:      true,
			wantFallback: true,
			wantInsecure: true,
		},
		{name: "require", sslMode: sslModeRequire, wantTLS: true, wantInsecure: true},
		{name: "verify full", sslMode: sslModeVerifyFull, wantTLS: true, wantRoots: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := driverConfig(runtimeSettings{
				username: "reader",
				password: "secret",
				host:     "db.example.com",
				port:     3306,
				database: "finance",
				sslMode:  test.sslMode,
			})
			if err != nil {
				t.Fatalf("driverConfig() error = %v", err)
			}

			if (cfg.TLS != nil) != test.wantTLS {
				t.Fatalf("TLS present = %v, want %v", cfg.TLS != nil, test.wantTLS)
			}

			if cfg.AllowFallbackToPlaintext != test.wantFallback {
				t.Fatalf("AllowFallbackToPlaintext = %v, want %v", cfg.AllowFallbackToPlaintext, test.wantFallback)
			}

			if cfg.TLS != nil {
				if cfg.TLS.ServerName != "db.example.com" {
					t.Fatalf("ServerName = %q, want db.example.com", cfg.TLS.ServerName)
				}

				if cfg.TLS.InsecureSkipVerify != test.wantInsecure {
					t.Fatalf("InsecureSkipVerify = %v, want %v", cfg.TLS.InsecureSkipVerify, test.wantInsecure)
				}

				if test.wantRoots && cfg.TLS.RootCAs == nil {
					t.Fatal("verify-full must use the system root pool")
				}
			}

			if cfg.MultiStatements || cfg.AllowAllFiles || cfg.AllowCleartextPasswords ||
				cfg.AllowOldPasswords || cfg.InterpolateParams {
				t.Fatal("unsafe driver options must remain disabled")
			}
		})
	}
}

type recordingPoolConfigurer struct {
	maxOpen     int
	maxIdle     int
	maxLifetime time.Duration
	maxIdleTime time.Duration
}

func (p *recordingPoolConfigurer) SetMaxOpenConns(value int)              { p.maxOpen = value }
func (p *recordingPoolConfigurer) SetMaxIdleConns(value int)              { p.maxIdle = value }
func (p *recordingPoolConfigurer) SetConnMaxLifetime(value time.Duration) { p.maxLifetime = value }
func (p *recordingPoolConfigurer) SetConnMaxIdleTime(value time.Duration) { p.maxIdleTime = value }

func TestConfigurePool(t *testing.T) {
	t.Parallel()

	configured := &recordingPoolConfigurer{}
	configurePool(configured)

	if configured.maxOpen != 4 || configured.maxIdle != 2 ||
		configured.maxLifetime != 30*time.Minute || configured.maxIdleTime != 5*time.Minute {
		t.Fatalf("pool config = %#v", configured)
	}
}
