package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/config"
)

func TestServiceEnvironmentIsAllowlistedAndOrdered(t *testing.T) {
	t.Setenv("DATAPORCH_MCP_TOKEN", "secret-canary")
	t.Setenv("UNRELATED_ENV", "unrelated-canary")
	cfg := config.Config{
		HTTPAddress:            "127.0.0.1:8080",
		ResourceLimit:          17,
		AdminSocketPath:        "/state/admin.sock",
		MasterKeyPath:          "/state/master.key",
		SecretsStorePath:       "/state/secrets.store",
		ConnectionsStorePath:   "/state/connections.store",
		MCPTokenStorePath:      "/state/mcp-token.json",
		QueryTimeout:           3 * time.Second,
		QueryResponseByteLimit: 4096,
		QueryTruncationEnabled: false,
		QueryRowLimit:          42,
	}
	wantNames := []string{
		"DATAPORCH_HTTP_ADDRESS",
		"DATAPORCH_RESOURCE_LIMIT",
		"DATAPORCH_ADMIN_SOCKET_PATH",
		"DATAPORCH_MASTER_KEY_PATH",
		"DATAPORCH_SECRETS_STORE_PATH",
		"DATAPORCH_CONNECTIONS_STORE_PATH",
		"DATAPORCH_MCP_TOKEN_STORE_PATH",
		"DATAPORCH_QUERY_TIMEOUT",
		"DATAPORCH_QUERY_RESPONSE_BYTE_LIMIT",
		"DATAPORCH_QUERY_TRUNCATION_ENABLED",
		"DATAPORCH_QUERY_ROW_LIMIT",
	}

	for range 2 {
		environment := serviceEnvironment(cfg)
		if len(environment) != len(wantNames) {
			t.Fatalf("serviceEnvironment() length = %d, want %d", len(environment), len(wantNames))
		}
		for index, variable := range environment {
			if variable.Name != wantNames[index] {
				t.Fatalf("environment[%d].Name = %q, want %q", index, variable.Name, wantNames[index])
			}
			if strings.Contains(variable.Name+variable.Value, "canary") {
				t.Fatalf("environment[%d] contains a process-environment canary: %#v", index, variable)
			}
		}
	}
}

func TestValidateDefinitionRejectsDuplicateAndUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		definition  ServiceDefinition
		wantMessage string
	}{
		{
			name: "missing executable",
			definition: ServiceDefinition{
				Environment: []EnvironmentVariable{{Name: "A", Value: "B"}},
			},
			wantMessage: "service executable is required",
		},
		{
			name: "duplicate environment",
			definition: ServiceDefinition{
				Executable:  "/bin/dataporch",
				Environment: []EnvironmentVariable{{Name: "A", Value: "one"}, {Name: "A", Value: "two"}},
			},
			wantMessage: "duplicate service environment",
		},
		{
			name:        "newline executable",
			definition:  ServiceDefinition{Executable: "/bin/data\nporch"},
			wantMessage: "newline",
		},
		{
			name: "nul environment",
			definition: ServiceDefinition{
				Executable:  "/bin/dataporch",
				Environment: []EnvironmentVariable{{Name: "A", Value: "bad\x00value"}},
			},
			wantMessage: "NUL",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateDefinition(test.definition)
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("validateDefinition() error = %v, want substring %q", err, test.wantMessage)
			}
		})
	}
}
