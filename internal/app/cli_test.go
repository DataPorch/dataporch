package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteCLIHelpDoesNotCreateRuntimeState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	originalStdout := os.Stdout
	originalStderr := os.Stderr
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutWriter
	os.Stderr = stderrWriter
	defer func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
		_ = stdoutReader.Close()
		_ = stderrReader.Close()
	}()

	code := ExecuteCLI(t.Context(), []string{"--help"})
	if err := stdoutWriter.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	if err := stderrWriter.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	stdout, err := io.ReadAll(stdoutReader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stderr, err := io.ReadAll(stderrReader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if code != 0 {
		t.Fatalf("ExecuteCLI() = %d, want 0; stderr=%q", code, stderr)
	}
	if !bytes.Contains(stdout, []byte("dataporch <command>")) {
		t.Fatalf("stdout = %q, want help output", stdout)
	}
	if len(stderr) != 0 {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".dataporch")); !os.IsNotExist(err) {
		t.Fatalf("default state directory stat error = %v, want not exist", err)
	}
}

func TestReadConfirmationLineTrimsInput(t *testing.T) {
	t.Parallel()

	file, err := os.CreateTemp(t.TempDir(), "confirmation")
	if err != nil {
		t.Fatalf("create input: %v", err)
	}
	if _, err := file.WriteString(" yes \n"); err != nil {
		t.Fatalf("write input: %v", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatalf("rewind input: %v", err)
	}
	value, err := readConfirmationLine(file)
	if err != nil {
		t.Fatalf("readConfirmationLine() error = %v", err)
	}
	if value != "yes" {
		t.Fatalf("readConfirmationLine() = %q, want yes", value)
	}
	if strings.TrimSpace(value) != value {
		t.Fatalf("confirmation was not trimmed: %q", value)
	}
}
