package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/DataPorch/dataporch/internal/config"
)

//nolint:dupword // Exact public help text intentionally repeats command names.
const wantRootHelp = "dataporch <command>\n\nUsage:\n\ndataporch run                 start the background user service\ndataporch run -f              run DataPorch in the current terminal\ndataporch restart             restart the background user service\ndataporch stop                stop the background user service\ndataporch status              display runtime status\ndataporch secrets init        initialize the local secret store\ndataporch connections import  import a database connection\ndataporch mcp                 connect an MCP client over stdio\ndataporch mcp-token <command> manage the local MCP token\ndataporch <command> -h        quick help on <command>\ndataporch -l                  display usage info for all commands\ndataporch help <command>      show help for an exact command\ndataporch help dataporch      show the complete overview\n\ndataporch@0.1.0 /opt/homebrew/bin/dataporch\n"

func TestRootHelpGoldenVariants(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{nil, {"-h"}, {"--help"}} {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		runner := testRunner(t, stdout, stderr)
		if got := runner.Execute(t.Context(), args); got != exitSuccess {
			t.Fatalf("Execute(%v) = %d, want %d", args, got, exitSuccess)
		}
		if stdout.String() != wantRootHelp {
			t.Fatalf("help = %q, want %q", stdout.String(), wantRootHelp)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want empty", stderr.String())
		}
	}
}

func TestDetailedAndExactHelpAreSideEffectFree(t *testing.T) {
	t.Parallel()

	var initializations, applications, managers int
	newRunner := func() (*Runner, *bytes.Buffer, *bytes.Buffer) {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		runner, err := New(Dependencies{
			Stdout: stdout, Stderr: stderr,
			LookupEnv: func(string) (string, bool) { return "", false },
			UserHomeDir: func() (string, error) {
				return "/Users/alice", nil
			},
			Version: "0.1.0", InvocationPath: "/opt/homebrew/bin/dataporch",
			InitializeSecrets: func(_ config.Config) error { initializations++; return nil },
			RunApplication:    func(context.Context, config.Config) error { applications++; return nil },
			NewServiceManager: func(config.Config) (ServiceManager, error) { managers++; return nil, nil },
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		return runner, stdout, stderr
	}

	for _, args := range [][]string{{"-l"}, {"help", "dataporch"}, {"help", "connections", "import"}, {"connections", "import", "-h"}} {
		runner, stdout, stderr := newRunner()
		if got := runner.Execute(t.Context(), args); got != exitSuccess {
			t.Fatalf("Execute(%v) = %d, want %d", args, got, exitSuccess)
		}
		if stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatalf("Execute(%v) streams = stdout %q stderr %q", args, stdout.String(), stderr.String())
		}
	}
	if initializations != 0 || applications != 0 || managers != 0 {
		t.Fatalf("help initialized runtime dependencies: init=%d app=%d managers=%d", initializations, applications, managers)
	}
}

func TestUnknownHelpTargetIsUsageError(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	runner := testRunner(t, stdout, stderr)
	if got := runner.Execute(t.Context(), []string{"help", "not-a-command"}); got != exitUsage {
		t.Fatalf("Execute() = %d, want %d", got, exitUsage)
	}
	if want := "dataporch: unknown help topic \"not-a-command\"; run dataporch -l\n"; stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestHelpRegistryListsNestedCommandsInStableOrder(t *testing.T) {
	t.Parallel()

	var previous string
	for _, command := range commandHelp {
		if strings.Compare(previous, command.path) == 0 {
			t.Fatalf("duplicate help path %q", command.path)
		}
		previous = command.path
	}
	if len(commandHelp) < 10 {
		t.Fatalf("help registry has %d entries, want complete command coverage", len(commandHelp))
	}
}
