package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/connection"
)

func TestSecretsInitRunsOnce(t *testing.T) {
	t.Parallel()

	dependencies := testCommandDependencies(t)
	initializations := 0
	dependencies.initializeSecrets = func(config.Config) error {
		initializations++
		return nil
	}

	if err := run([]string{"secrets", "init"}, dependencies); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if initializations != 1 {
		t.Fatalf("initializations = %d, want 1", initializations)
	}
}

func TestConnectionsImportRequiresID(t *testing.T) {
	t.Parallel()

	err := run([]string{"connections", "import", "--kind", "postgres"}, testCommandDependencies(t))
	if !errors.Is(err, errDatabaseIDRequired) {
		t.Fatalf("run() error = %v, want %v", err, errDatabaseIDRequired)
	}
}

func TestConnectionsImportRequiresKind(t *testing.T) {
	t.Parallel()

	err := run([]string{"connections", "import", "--id", "finance"}, testCommandDependencies(t))
	if !errors.Is(err, errDatabaseKindRequired) {
		t.Fatalf("run() error = %v, want %v", err, errDatabaseKindRequired)
	}
}

func TestConnectionsImportRequiresTerminal(t *testing.T) {
	t.Parallel()

	dependencies := testCommandDependencies(t)
	dependencies.isTerminal = func(int) bool { return false }
	err := run([]string{"connections", "import", "--id", "finance", "--kind", "postgres"}, dependencies)
	if !errors.Is(err, errTerminalRequired) {
		t.Fatalf("run() error = %v, want %v", err, errTerminalRequired)
	}
}

func TestConnectionsImportReadsHiddenValue(t *testing.T) {
	t.Parallel()

	dependencies := testCommandDependencies(t)
	canary := []byte("postgres://reader:password@host/finance")
	dependencies.readPassword = func(int) ([]byte, error) { return append([]byte(nil), canary...), nil }
	var got connection.ImportRequest
	dependencies.newClient = func(string) (importClient, error) {
		return importClientFunc(func(_ context.Context, request connection.ImportRequest) (connection.ImportResult, error) {
			got = connection.ImportRequest{
				ID:               request.ID,
				Kind:             request.Kind,
				ConnectionString: append([]byte(nil), request.ConnectionString...),
			}
			return connection.ImportResult{ID: "finance"}, nil
		}), nil
	}

	if err := run([]string{"connections", "import", "--id", "finance", "--kind", "postgres"}, dependencies); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if string(got.ConnectionString) != string(canary) {
		t.Fatalf("connection string = %q, want %q", got.ConnectionString, canary)
	}
}

func TestConnectionsImportDoesNotAcceptDSNFlag(t *testing.T) {
	t.Parallel()

	canary := "postgres://reader:password@host/finance"
	dependencies := testCommandDependencies(t)
	err := run([]string{"connections", "import", "--id", "finance", "--kind", "postgres", "--dsn", canary}, dependencies)
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("run() error = %v, want safe unknown-flag error", err)
	}
}

func TestConnectionsImportPrintsExactAddedMessage(t *testing.T) {
	t.Parallel()

	dependencies := testCommandDependencies(t)
	dependencies.newClient = resultClientFactory(connection.ImportResult{ID: "finance"})
	if err := run([]string{"connections", "import", "--id", "finance", "--kind", "postgres"}, dependencies); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	const want = "Database \"finance\" was added successfully and its connection has not been tested.\n"
	if got := dependencies.stdout.(*bytes.Buffer).String(); got != "Connection string: \n"+want {
		t.Fatalf("stdout = %q, want %q", got, "Connection string: \n"+want)
	}
}

func TestConnectionsImportPrintsUpdatedMessage(t *testing.T) {
	t.Parallel()

	dependencies := testCommandDependencies(t)
	dependencies.newClient = resultClientFactory(connection.ImportResult{ID: "finance", Updated: true})
	if err := run([]string{"connections", "import", "--id", "finance", "--kind", "postgres"}, dependencies); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	const want = "Database \"finance\" was updated successfully and its connection has not been tested.\n"
	if got := dependencies.stdout.(*bytes.Buffer).String(); got != "Connection string: \n"+want {
		t.Fatalf("stdout = %q, want %q", got, "Connection string: \n"+want)
	}
}

func TestConnectionsImportDoesNotEchoCanary(t *testing.T) {
	t.Parallel()

	canary := "postgres://reader:password@host/finance"
	dependencies := testCommandDependencies(t)
	dependencies.readPassword = func(int) ([]byte, error) { return []byte(canary), nil }
	dependencies.newClient = func(string) (importClient, error) {
		return importClientFunc(func(context.Context, connection.ImportRequest) (connection.ImportResult, error) {
			return connection.ImportResult{}, errors.New("request rejected")
		}), nil
	}

	err := run([]string{"connections", "import", "--id", "finance", "--kind", "postgres"}, dependencies)
	if err == nil {
		t.Fatal("run() error = nil, want error")
	}
	if strings.Contains(dependencies.stdout.(*bytes.Buffer).String(), canary) ||
		strings.Contains(dependencies.stderr.(*bytes.Buffer).String(), canary) ||
		strings.Contains(err.Error(), canary) {
		t.Fatal("connection string leaked")
	}
}

func testCommandDependencies(t *testing.T) commandDependencies {
	t.Helper()
	return commandDependencies{
		stdin:        os.Stdin,
		stdout:       &bytes.Buffer{},
		stderr:       &bytes.Buffer{},
		lookupEnv:    func(string) (string, bool) { return "", false },
		isTerminal:   func(int) bool { return true },
		readPassword: func(int) ([]byte, error) { return []byte("postgres://reader:password@host/finance"), nil },
		initializeSecrets: func(config.Config) error {
			return nil
		},
		runApplication: func(context.Context, config.Config) error { return nil },
		newClient:      resultClientFactory(connection.ImportResult{ID: "finance"}),
	}
}

func resultClientFactory(result connection.ImportResult) func(string) (importClient, error) {
	return func(string) (importClient, error) {
		return importClientFunc(
			func(context.Context, connection.ImportRequest) (connection.ImportResult, error) {
				return result, nil
			},
		), nil
	}
}
