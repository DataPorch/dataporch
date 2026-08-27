package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/connection"
)

func TestMCPCommandPassesContextAndStreams(t *testing.T) {
	t.Parallel()

	dependencies := testCommandDependencies(t)
	dependencies.protectedFileValidator = func(string) error { return nil }
	adapter := &recordingMCPAdapter{}
	dependencies.runMCPAdapter = adapter.Run
	ctx := t.Context()
	if err := runWithContext(ctx, []string{"mcp"}, dependencies); err != nil {
		t.Fatalf("runWithContext() error = %v", err)
	}
	if adapter.context != ctx {
		t.Fatal("MCP adapter did not receive caller context")
	}
	if adapter.config.MCPSocketPath == "" || adapter.config.MCPControlTokenPath == "" {
		t.Fatalf("MCP config paths = %#v, want resolved paths", adapter.config)
	}
	if adapter.input != dependencies.stdin || adapter.output != dependencies.stdout {
		t.Fatal("MCP adapter did not receive injected streams")
	}
	stdout, ok := dependencies.stdout.(*bytes.Buffer)
	if !ok {
		t.Fatalf("stdout type = %T, want *bytes.Buffer", dependencies.stdout)
	}
	if stdout.Len() != 0 {
		t.Fatal("MCP command wrote non-protocol stdout")
	}
}

type recordingMCPAdapter struct {
	context context.Context //nolint:containedctx // The test records the exact caller context.
	config  config.Config
	input   io.Reader
	output  io.Writer
}

func (a *recordingMCPAdapter) Run(ctx context.Context, cfg config.Config, input io.Reader, output io.Writer) error {
	a.context, a.config, a.input, a.output = ctx, cfg, input, output
	return nil
}

func TestMCPCommandRejectsExtraArguments(t *testing.T) {
	t.Parallel()

	err := run([]string{"mcp", "extra"}, testCommandDependencies(t))
	if !errors.Is(err, errUnexpectedArguments) {
		t.Fatalf("run() error = %v, want unexpected arguments", err)
	}
	if got := exitCode(err); got != exitUsage {
		t.Fatalf("exitCode() = %d, want %d", got, exitUsage)
	}
}

func TestMCPCommandDiagnosticsStayOnStderr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		validator  ProtectedFileValidator
		adapterErr error
		want       string
	}{
		{
			name: "not initialized",
			validator: func(string) error {
				return fs.ErrNotExist
			},
			want: "dataporch: not initialized; run dataporch secrets init, then dataporch run\n",
		},
		{
			name:       "runtime stopped",
			validator:  func(string) error { return nil },
			adapterErr: ErrMCPRuntimeUnavailable,
			want:       "dataporch: runtime is not running; run dataporch run\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			dependencies := testCommandDependencies(t)
			dependencies.stdout = stdout
			dependencies.stderr = stderr
			dependencies.protectedFileValidator = test.validator
			dependencies.runMCPAdapter = func(context.Context, config.Config, io.Reader, io.Writer) error {
				return test.adapterErr
			}
			runner, err := New(Dependencies{
				Stdin: dependencies.stdin, Stdout: stdout, Stderr: stderr,
				LookupEnv: dependencies.lookupEnv, UserHomeDir: dependencies.userHomeDir,
				ProtectedFileValidator: dependencies.protectedFileValidator,
				RunMCPAdapter:          dependencies.runMCPAdapter,
				Version:                "0.1.0", InvocationPath: "/opt/homebrew/bin/dataporch",
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if got := runner.Execute(t.Context(), []string{"mcp"}); got != exitFailure {
				t.Fatalf("Execute() = %d, want failure", got)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if stderr.String() != test.want {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
		})
	}
}

func TestSecretsInitRunsOnce(t *testing.T) {
	t.Parallel()

	dependencies := testCommandDependencies(t)

	var initializations int

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

	stdout, ok := dependencies.stdout.(*bytes.Buffer)
	if !ok {
		t.Fatalf("stdout type = %T, want *bytes.Buffer", dependencies.stdout)
	}

	if got := stdout.String(); got != "Connection string: \n"+want {
		t.Fatalf("stdout = %q, want %q", got, "Connection string: \n"+want)
	}
}

func TestConnectionsImportPrintsUpdatedMessage(t *testing.T) {
	t.Parallel()

	dependencies := testCommandDependencies(t)

	dependencies.newClient = resultClientFactory(connection.ImportResult{ID: "finance", IsUpdated: true})
	if err := run([]string{"connections", "import", "--id", "finance", "--kind", "postgres"}, dependencies); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	const want = "Database \"finance\" was updated successfully and its connection has not been tested.\n"

	stdout, ok := dependencies.stdout.(*bytes.Buffer)
	if !ok {
		t.Fatalf("stdout type = %T, want *bytes.Buffer", dependencies.stdout)
	}

	if got := stdout.String(); got != "Connection string: \n"+want {
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

	stdout, stdoutOK := dependencies.stdout.(*bytes.Buffer)

	stderr, stderrOK := dependencies.stderr.(*bytes.Buffer)
	if !stdoutOK || !stderrOK {
		t.Fatalf("stdout/stderr types = %T/%T, want *bytes.Buffer", dependencies.stdout, dependencies.stderr)
	}

	stdoutContainsCanary := strings.Contains(stdout.String(), canary)
	stderrContainsCanary := strings.Contains(stderr.String(), canary)

	errorContainsCanary := strings.Contains(err.Error(), canary)
	if stdoutContainsCanary || stderrContainsCanary || errorContainsCanary {
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
