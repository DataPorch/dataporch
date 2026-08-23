package cli

import (
	"bytes"
	"errors"
	"testing"
)

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestExitCodeClassifiesCLIResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "success", want: exitSuccess},
		{name: "failure", err: errors.New("runtime failure"), want: exitFailure},
		{name: "usage", err: usageError("bad usage", nil), want: exitUsage},
		{name: "stopped", err: stoppedResult(), want: exitStopped},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := exitCode(test.err); got != test.want {
				t.Fatalf("exitCode(%v) = %d, want %d", test.err, got, test.want)
			}
		})
	}
}

func TestExecuteReturnsFailureWhenSuccessfulOutputCannotBeWritten(t *testing.T) {
	t.Parallel()

	runner, err := New(Dependencies{
		Stdout:         errorWriter{err: errors.New("stdout closed")},
		Stderr:         &bytes.Buffer{},
		LookupEnv:      func(string) (string, bool) { return "", false },
		Version:        "0.1.0",
		InvocationPath: "/opt/homebrew/bin/dataporch",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := runner.Execute(t.Context(), []string{"--version"}); got != exitFailure {
		t.Fatalf("Execute() = %d, want %d", got, exitFailure)
	}
}

func TestExecuteReturnsFailureWhenDiagnosticCannotBeWritten(t *testing.T) {
	t.Parallel()

	runner, err := New(Dependencies{
		Stdout:         &bytes.Buffer{},
		Stderr:         errorWriter{err: errors.New("stderr closed")},
		LookupEnv:      func(string) (string, bool) { return "", false },
		Version:        "0.1.0",
		InvocationPath: "/opt/homebrew/bin/dataporch",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := runner.Execute(t.Context(), []string{"unknown"}); got != exitFailure {
		t.Fatalf("Execute() = %d, want %d", got, exitFailure)
	}
}
