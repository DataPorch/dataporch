package cli

import (
	"bytes"
	"context"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/adamraziv/dataporch/internal/config"
)

func TestRootHelpVariantsAreIdentical(t *testing.T) {
	t.Parallel()

	const wantFooter = "dataporch@0.1.0 /opt/homebrew/bin/dataporch\n"
	var want string
	for _, args := range [][]string{nil, []string{"-h"}, []string{"--help"}} {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		runner := testRunner(t, stdout, stderr)

		if got := runner.Execute(t.Context(), args); got != exitSuccess {
			t.Fatalf("Execute(%v) = %d, want %d", args, got, exitSuccess)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want empty", stderr.String())
		}
		if !strings.HasSuffix(stdout.String(), wantFooter) {
			t.Fatalf("help footer = %q, want suffix %q", stdout.String(), wantFooter)
		}
		if want == "" {
			want = stdout.String()
		} else if stdout.String() != want {
			t.Fatalf("help for %v differs from bare help", args)
		}
	}
}

func TestExecuteClassifiesUsageAndForeground(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args       []string
		wantCode   int
		wantStderr string
	}{
		{args: []string{"start"}, wantCode: exitUsage, wantStderr: "dataporch: unknown command \"start\"; run dataporch --help\n"},
		{args: []string{"run", "--foreground"}, wantCode: exitUsage, wantStderr: "dataporch: unknown flag --foreground; run dataporch run -h\n"},
	}
	for _, test := range tests {
		test := test
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			t.Parallel()
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			runner := testRunner(t, stdout, stderr)
			if got := runner.Execute(t.Context(), test.args); got != test.wantCode {
				t.Fatalf("Execute(%v) = %d, want %d", test.args, got, test.wantCode)
			}
			if got := stderr.String(); got != test.wantStderr {
				t.Fatalf("stderr = %q, want %q", got, test.wantStderr)
			}
		})
	}
}

func TestRunForegroundUsesCallerContext(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	called := false
	runner, err := New(Dependencies{
		Stdout: stdout, Stderr: stderr, LookupEnv: func(string) (string, bool) { return "", false },
		UserHomeDir: func() (string, error) { return t.TempDir(), nil },
		Version:     "0.1.0", InvocationPath: "/opt/homebrew/bin/dataporch",
		RunApplication: func(ctx context.Context, _ config.Config) error {
			called = true
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := runner.Execute(ctx, []string{"run", "-f"}); got != exitFailure {
		t.Fatalf("Execute() = %d, want %d", got, exitFailure)
	}
	if !called {
		t.Fatal("foreground runner was not called")
	}
	if !strings.Contains(stderr.String(), context.Canceled.Error()) {
		t.Fatalf("stderr = %q, want canceled context", stderr.String())
	}
}

func TestResolvedVersion(t *testing.T) {
	t.Parallel()

	if got := resolvedVersion("v0.1.0", func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}}, true }); got != "0.1.0" {
		t.Fatalf("resolvedVersion() = %q, want 0.1.0", got)
	}
	if got := resolvedVersion("", func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true }); got != "devel" {
		t.Fatalf("resolvedVersion() = %q, want devel", got)
	}
}

func testRunner(t *testing.T, stdout, stderr *bytes.Buffer) *Runner {
	t.Helper()
	runner, err := New(Dependencies{
		Stdout: stdout, Stderr: stderr, LookupEnv: func(string) (string, bool) { return "", false },
		UserHomeDir: func() (string, error) { return t.TempDir(), nil },
		Version:     "0.1.0", InvocationPath: "/opt/homebrew/bin/dataporch",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return runner
}
