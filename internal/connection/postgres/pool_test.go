package postgres

import (
	"errors"
	"os"
	"testing"

	"github.com/adamraziv/dataporch/internal/connection"
)

func TestPGXPoolFactoryConfigUsesResolvedDefinition(t *testing.T) {
	t.Parallel()

	factory, err := newPGXPoolFactory()
	if err != nil {
		t.Fatalf("newPGXPoolFactory() error = %v", err)
	}

	definition := resolvedPostgresDefinition()

	config, err := factory.config(definition)
	if err != nil {
		t.Fatalf("config() error = %v", err)
	}

	if config.ConnConfig.Host != "postgres.internal" {
		t.Errorf("Host = %q, want postgres.internal", config.ConnConfig.Host)
	}

	if config.ConnConfig.Port != 6543 {
		t.Errorf("Port = %d, want 6543", config.ConnConfig.Port)
	}

	if config.ConnConfig.Database != "finance" {
		t.Errorf("Database = %q, want finance", config.ConnConfig.Database)
	}

	if config.ConnConfig.User != "app_reader" {
		t.Errorf("User = %q, want app_reader", config.ConnConfig.User)
	}

	if config.ConnConfig.Password != "runtime-secret-canary" {
		t.Errorf("Password = %q, want runtime-secret-canary", config.ConnConfig.Password)
	}

	if config.ConnConfig.TLSConfig == nil || config.ConnConfig.TLSConfig.ServerName != "postgres.internal" {
		t.Errorf("TLSConfig = %#v, want verify-full config for postgres.internal", config.ConnConfig.TLSConfig)
	}

	if len(config.ConnConfig.Fallbacks) != 0 {
		t.Errorf("Fallbacks length = %d, want 0", len(config.ConnConfig.Fallbacks))
	}

	if len(config.ConnConfig.RuntimeParams) != 0 {
		t.Errorf("RuntimeParams = %#v, want empty", config.ConnConfig.RuntimeParams)
	}
}

func TestPGXPoolFactoryConfigPermitsControlCharactersInDatabase(t *testing.T) {
	t.Parallel()

	factory, err := newPGXPoolFactory()
	if err != nil {
		t.Fatalf("newPGXPoolFactory() error = %v", err)
	}

	definition := resolvedPostgresDefinition()
	definition.Settings[settingDatabase] = "finance\narchive"

	config, err := factory.config(definition)
	if err != nil {
		t.Fatalf("config() error = %v", err)
	}

	if config.ConnConfig.Database != "finance\narchive" {
		t.Errorf("Database = %q, want finance\\narchive", config.ConnConfig.Database)
	}
}

func TestPGXPoolFactoryConfigAppliesRuntimeDefaults(t *testing.T) {
	t.Parallel()

	factory, err := newPGXPoolFactory()
	if err != nil {
		t.Fatalf("newPGXPoolFactory() error = %v", err)
	}

	definition := resolvedPostgresDefinition()
	delete(definition.Settings, settingPort)
	delete(definition.Settings, settingSSLMode)

	config, err := factory.config(definition)
	if err != nil {
		t.Fatalf("config() error = %v", err)
	}

	if config.ConnConfig.Port != 5432 {
		t.Errorf("Port = %d, want 5432", config.ConnConfig.Port)
	}

	if config.ConnConfig.TLSConfig == nil || !config.ConnConfig.TLSConfig.InsecureSkipVerify {
		t.Errorf("TLSConfig = %#v, want prefer primary TLS config", config.ConnConfig.TLSConfig)
	}

	if config.ConnConfig.Fallbacks == nil || len(config.ConnConfig.Fallbacks) != 1 {
		t.Fatalf("Fallbacks = %#v, want one fallback", config.ConnConfig.Fallbacks)
	}

	if config.ConnConfig.Fallbacks[0].TLSConfig != nil {
		t.Errorf("Fallback TLSConfig = %#v, want nil", config.ConnConfig.Fallbacks[0].TLSConfig)
	}

	if _, exists := definition.Settings[settingPort]; exists {
		t.Error("config() persisted the port default")
	}

	if _, exists := definition.Settings[settingSSLMode]; exists {
		t.Error("config() persisted the sslmode default")
	}
}

func TestPGXPoolFactoryConfigRejectsMalformedDefinitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*connection.ResolvedDefinition)
	}{
		{name: "missing username", mutate: func(definition *connection.ResolvedDefinition) {
			delete(definition.Settings, settingUsername)
		}},
		{name: "missing host", mutate: func(definition *connection.ResolvedDefinition) {
			delete(definition.Settings, settingHost)
		}},
		{name: "missing database", mutate: func(definition *connection.ResolvedDefinition) {
			delete(definition.Settings, settingDatabase)
		}},
		{name: "extra setting", mutate: func(definition *connection.ResolvedDefinition) {
			definition.Settings["unexpected"] = "value"
		}},
		{name: "missing secret", mutate: func(definition *connection.ResolvedDefinition) {
			delete(definition.Secrets, settingPassword)
		}},
		{name: "extra secret", mutate: func(definition *connection.ResolvedDefinition) {
			definition.Secrets["unexpected"] = []byte("value")
		}},
		{name: "empty username", mutate: func(definition *connection.ResolvedDefinition) {
			definition.Settings[settingUsername] = ""
		}},
		{name: "empty password", mutate: func(definition *connection.ResolvedDefinition) {
			definition.Secrets[settingPassword] = []byte{}
		}},
		{name: "nul host", mutate: func(definition *connection.ResolvedDefinition) {
			definition.Settings[settingHost] = "postgres\x00.internal"
		}},
		{name: "nul password", mutate: func(definition *connection.ResolvedDefinition) {
			definition.Secrets[settingPassword] = []byte("secret\x00canary")
		}},
		{name: "zero port", mutate: func(definition *connection.ResolvedDefinition) {
			definition.Settings[settingPort] = "0"
		}},
		{name: "port above maximum", mutate: func(definition *connection.ResolvedDefinition) {
			definition.Settings[settingPort] = "65536"
		}},
		{name: "nonnumeric port", mutate: func(definition *connection.ResolvedDefinition) {
			definition.Settings[settingPort] = "postgres"
		}},
		{name: "invalid sslmode", mutate: func(definition *connection.ResolvedDefinition) {
			definition.Settings[settingSSLMode] = "verify-ca-without-verification"
		}},
		{name: "invalid id", mutate: func(definition *connection.ResolvedDefinition) {
			definition.ID = "not valid"
		}},
		{name: "non postgres kind", mutate: func(definition *connection.ResolvedDefinition) {
			definition.Kind = "mysql"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			factory, err := newPGXPoolFactory()
			if err != nil {
				t.Fatalf("newPGXPoolFactory() error = %v", err)
			}

			definition := resolvedPostgresDefinition()
			test.mutate(&definition)

			if _, err := factory.config(definition); !errors.Is(err, errInvalidRuntimeDefinition) {
				t.Fatalf("config() error = %v, want errInvalidRuntimeDefinition", err)
			}
		})
	}
}

func TestPGXPoolFactoryConfigPreservesPoolDefaults(t *testing.T) {
	t.Parallel()

	factory, err := newPGXPoolFactory()
	if err != nil {
		t.Fatalf("newPGXPoolFactory() error = %v", err)
	}

	config, err := factory.config(resolvedPostgresDefinition())
	if err != nil {
		t.Fatalf("config() error = %v", err)
	}

	if config.MaxConns != factory.template.MaxConns {
		t.Errorf("MaxConns = %d, want template default %d", config.MaxConns, factory.template.MaxConns)
	}

	if config.MinConns != factory.template.MinConns {
		t.Errorf("MinConns = %d, want template default %d", config.MinConns, factory.template.MinConns)
	}

	if config.MaxConnIdleTime != factory.template.MaxConnIdleTime {
		t.Errorf("MaxConnIdleTime = %s, want template default %s", config.MaxConnIdleTime, factory.template.MaxConnIdleTime)
	}

	if config.MaxConns == 0 || config.MaxConnIdleTime == 0 {
		t.Fatal("template did not retain pgx pool defaults")
	}
}

func TestPGXPoolFactoryConfigBuildsSSLMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		mode             string
		wantFallbacks    int
		wantPrimaryTLS   bool
		wantPrimaryInsec bool
	}{
		{name: "disable", mode: "disable", wantFallbacks: 0},
		{name: "allow", mode: "allow", wantFallbacks: 1},
		{name: "prefer", mode: "prefer", wantFallbacks: 1, wantPrimaryTLS: true, wantPrimaryInsec: true},
		{name: "require", mode: "require", wantFallbacks: 0, wantPrimaryTLS: true, wantPrimaryInsec: true},
		{name: "verify ca", mode: "verify-ca", wantFallbacks: 0, wantPrimaryTLS: true},
		{name: "verify full", mode: "verify-full", wantFallbacks: 0, wantPrimaryTLS: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			factory, err := newPGXPoolFactory()
			if err != nil {
				t.Fatalf("newPGXPoolFactory() error = %v", err)
			}

			definition := resolvedPostgresDefinition()
			definition.Settings[settingSSLMode] = test.mode

			config, err := factory.config(definition)
			if err != nil {
				t.Fatalf("config() error = %v", err)
			}

			if len(config.ConnConfig.Fallbacks) != test.wantFallbacks {
				t.Errorf("Fallbacks length = %d, want %d", len(config.ConnConfig.Fallbacks), test.wantFallbacks)
			}

			if (config.ConnConfig.TLSConfig != nil) != test.wantPrimaryTLS {
				t.Errorf("TLSConfig present = %t, want %t", config.ConnConfig.TLSConfig != nil, test.wantPrimaryTLS)
			}

			if test.wantPrimaryInsec && !config.ConnConfig.TLSConfig.InsecureSkipVerify {
				t.Error("primary TLS config is not intentionally insecure")
			}

			if test.mode == "verify-ca" && config.ConnConfig.TLSConfig.VerifyConnection == nil {
				t.Error("verify-ca TLS config has no VerifyConnection callback")
			}

			if test.mode == "verify-full" && config.ConnConfig.TLSConfig.InsecureSkipVerify {
				t.Error("verify-full TLS config disables hostname verification")
			}
		})
	}
}

func TestNewPGXPoolFactoryIgnoresPostgresEnvironment(t *testing.T) {
	wantEnvironment := make(map[string]string, len(postgresEnvironmentVariables()))
	for _, name := range postgresEnvironmentVariables() {
		value := "attacker-" + name
		t.Setenv(name, value)
		wantEnvironment[name] = value
	}

	t.Setenv("PGSERVICE", "/does/not/exist/service.conf")
	t.Setenv("PGPASSFILE", "/does/not/exist/passfile")
	t.Setenv("PGSSLKEY", "/does/not/exist/client.key")
	t.Setenv("PGSSLCERT", "/does/not/exist/client.crt")
	t.Setenv("PGSSLROOTCERT", "/does/not/exist/root.crt")

	wantEnvironment["PGSERVICE"] = "/does/not/exist/service.conf"
	wantEnvironment["PGPASSFILE"] = "/does/not/exist/passfile"
	wantEnvironment["PGSSLKEY"] = "/does/not/exist/client.key"
	wantEnvironment["PGSSLCERT"] = "/does/not/exist/client.crt"
	wantEnvironment["PGSSLROOTCERT"] = "/does/not/exist/root.crt"

	factory, err := newPGXPoolFactory()
	if err != nil {
		t.Fatalf("newPGXPoolFactory() error = %v", err)
	}

	for _, name := range postgresEnvironmentVariables() {
		got, exists := os.LookupEnv(name)

		want := wantEnvironment[name]
		if !exists || got != want {
			t.Errorf("environment %s = %q, exists %t, want %q, true", name, got, exists, want)
		}
	}

	config, err := factory.config(resolvedPostgresDefinition())
	if err != nil {
		t.Fatalf("config() error = %v", err)
	}

	if config.ConnConfig.Host != "postgres.internal" ||
		config.ConnConfig.Port != 6543 ||
		config.ConnConfig.Database != "finance" ||
		config.ConnConfig.User != "app_reader" ||
		config.ConnConfig.Password != "runtime-secret-canary" {
		t.Errorf("config contains non-fixture connection values: %#v", config.ConnConfig)
	}
}

func resolvedPostgresDefinition() connection.ResolvedDefinition {
	return connection.ResolvedDefinition{
		ID:   "finance",
		Kind: Kind,
		Settings: map[string]string{
			settingUsername: "app_reader",
			settingHost:     "postgres.internal",
			settingPort:     "6543",
			settingDatabase: "finance",
			settingSSLMode:  "verify-full",
		},
		Secrets: map[string][]byte{
			settingPassword: []byte("runtime-secret-canary"),
		},
	}
}
