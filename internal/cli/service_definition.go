package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/adamraziv/dataporch/internal/config"
)

type NativeState string

const (
	NativeRunning NativeState = "running"
	NativeStopped NativeState = "stopped"
	NativeFailed  NativeState = "failed"
)

type NativeStatus struct {
	Registered bool
	State      NativeState
	PID        int
}
type ServiceDefinition struct {
	Executable             string
	Arguments              []string
	Environment            []EnvironmentVariable
	StdoutPath, StderrPath string
}
type (
	EnvironmentVariable struct{ Name, Value string }
	ServiceManager      interface {
		Register(context.Context, ServiceDefinition) error
		Start(context.Context) error
		Restart(context.Context) error
		Stop(context.Context) error
		Unregister(context.Context) error
		Status(context.Context) (NativeStatus, error)
		DefinitionPath() string
		LogLocation() string
	}
)
type ProtectedFileValidator func(string) error

func serviceEnvironment(cfg config.Config) []EnvironmentVariable {
	return []EnvironmentVariable{
		{"DATAPORCH_HTTP_ADDRESS", cfg.HTTPAddress},
		{"DATAPORCH_RESOURCE_LIMIT", strconv.Itoa(cfg.ResourceLimit)},
		{"DATAPORCH_ADMIN_SOCKET_PATH", cfg.AdminSocketPath},
		{"DATAPORCH_MCP_SOCKET_PATH", cfg.MCPSocketPath},
		{"DATAPORCH_MASTER_KEY_PATH", cfg.MasterKeyPath},
		{"DATAPORCH_SECRETS_STORE_PATH", cfg.SecretsStorePath},
		{"DATAPORCH_CONNECTIONS_STORE_PATH", cfg.ConnectionsStorePath},
		{"DATAPORCH_MCP_TOKEN_STORE_PATH", cfg.MCPTokenStorePath},
		{"DATAPORCH_MCP_CONTROL_TOKEN_PATH", cfg.MCPControlTokenPath},
		{"DATAPORCH_QUERY_TIMEOUT", cfg.QueryTimeout.String()},
		{"DATAPORCH_QUERY_RESPONSE_BYTE_LIMIT", strconv.Itoa(cfg.QueryResponseByteLimit)},
		{"DATAPORCH_QUERY_TRUNCATION_ENABLED", strconv.FormatBool(cfg.QueryTruncationEnabled)},
		{"DATAPORCH_QUERY_ROW_LIMIT", strconv.Itoa(cfg.QueryRowLimit)},
	}
}

func validateDefinition(definition ServiceDefinition) error {
	if definition.Executable == "" {
		return errors.New("service executable is required")
	}
	if err := validateServiceValue("service executable", definition.Executable); err != nil {
		return err
	}
	for index, argument := range definition.Arguments {
		if err := validateServiceValue(fmt.Sprintf("service argument %d", index), argument); err != nil {
			return err
		}
	}
	seen := map[string]struct{}{}
	for _, variable := range definition.Environment {
		if variable.Name == "" {
			return errors.New("service environment name is required")
		}
		if strings.Contains(variable.Name, "=") {
			return fmt.Errorf("service environment name %q must not contain =", variable.Name)
		}
		if err := validateServiceValue("service environment name", variable.Name); err != nil {
			return err
		}
		if err := validateServiceValue("service environment value", variable.Value); err != nil {
			return err
		}
		if _, ok := seen[variable.Name]; ok {
			return fmt.Errorf("duplicate service environment %q", variable.Name)
		}
		seen[variable.Name] = struct{}{}
	}
	if err := validateServiceValue("service stdout path", definition.StdoutPath); err != nil {
		return err
	}
	if err := validateServiceValue("service stderr path", definition.StderrPath); err != nil {
		return err
	}
	return nil
}

func validateServiceValue(label, value string) error {
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s contains NUL", label)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s contains newline", label)
	}
	return nil
}
