package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestRenderSystemdUnitQuotesArgumentsAndEnvironment(t *testing.T) {
	t.Parallel()

	definition := ServiceDefinition{
		Executable:  `/Users/Alice/Data Porch/$()/dataporch%;"\\`,
		Arguments:   []string{"run", "-f"},
		Environment: []EnvironmentVariable{{Name: "DATAPORCH_HTTP_ADDRESS", Value: `127.0.0.1:8080;$(echo bad)&<>%"\`}},
	}
	data, err := renderSystemdUnit(definition)
	if err != nil {
		t.Fatalf("renderSystemdUnit() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "Restart=no") || strings.Contains(text, "[Install]") || strings.Contains(text, "WantedBy=") {
		t.Fatalf("unit has unexpected lifecycle directives: %s", text)
	}
	if !strings.Contains(text, "%%") {
		t.Fatalf("unit did not escape systemd percent specifiers: %s", text)
	}
	args := parseSystemdExecStart(t, text)
	if want := []string{definition.Executable, "run", "-f"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("ExecStart = %#v, want %#v", args, want)
	}
	environment := parseSystemdEnvironment(t, text)
	if got := environment[definition.Environment[0].Name]; got != definition.Environment[0].Value {
		t.Fatalf("environment value = %q, want %q", got, definition.Environment[0].Value)
	}
	for _, forbidden := range []string{"/bin/sh", " -c", "secret-canary", "unrelated-canary"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unit contains forbidden value %q: %s", forbidden, text)
		}
	}
}

func TestRenderSystemdUnitRejectsControlCharacters(t *testing.T) {
	t.Parallel()

	for _, definition := range []ServiceDefinition{
		{Executable: "/bin/dataporch\nrun"},
		{Executable: "/bin/dataporch", Environment: []EnvironmentVariable{{Name: "A", Value: "bad\x00value"}}},
	} {
		if _, err := renderSystemdUnit(definition); err == nil {
			t.Fatalf("renderSystemdUnit(%#v) error = nil", definition)
		}
	}
}

func TestSystemdManagerUsesExplicitCommands(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runner := &fakeCommandRunner{}
	manager, err := newSystemdManager(home, runner)
	if err != nil {
		t.Fatalf("newSystemdManager() error = %v", err)
	}
	definition := ServiceDefinition{Executable: "/opt/dataporch", Arguments: []string{"run", "-f"}}
	if err := manager.Register(t.Context(), definition); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	assertCommandCalls(t, runner.calls, []commandCall{{name: "systemctl", args: []string{"--user", "daemon-reload"}}})

	runner.calls = nil
	if err := manager.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	assertCommandCalls(t, runner.calls, []commandCall{{name: "systemctl", args: []string{"--user", "start", "dataporch.service"}}})

	runner.calls = nil
	if err := manager.Restart(t.Context()); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	assertCommandCalls(t, runner.calls, []commandCall{
		{name: "systemctl", args: []string{"--user", "daemon-reload"}},
		{name: "systemctl", args: []string{"--user", "restart", "dataporch.service"}},
	})

	runner.calls = nil
	if err := manager.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	assertCommandCalls(t, runner.calls, []commandCall{{name: "systemctl", args: []string{"--user", "stop", "dataporch.service"}}})
}

func TestSystemdManagerTreatsAbsentServiceAsSuccess(t *testing.T) {
	t.Parallel()

	runner := &fakeCommandRunner{run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Unit dataporch.service not loaded."), errors.New("exit status 5")
	}}
	manager, err := newSystemdManager(t.TempDir(), runner)
	if err != nil {
		t.Fatalf("newSystemdManager() error = %v", err)
	}
	if err := manager.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v, want nil for absent service", err)
	}
}

func TestSystemdManagerRegistersAndUnregistersOnlyDefinition(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runner := &fakeCommandRunner{}
	manager, err := newSystemdManager(home, runner)
	if err != nil {
		t.Fatalf("newSystemdManager() error = %v", err)
	}
	if err := manager.Register(t.Context(), ServiceDefinition{Executable: "/opt/dataporch"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	info, err := os.Stat(manager.DefinitionPath())
	if err != nil {
		t.Fatalf("definition stat error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("definition mode = %o, want 600", got)
	}
	statePath := filepath.Join(home, ".dataporch", "secrets.store")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	if err := os.WriteFile(statePath, []byte("state"), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	runner.calls = nil
	if err := manager.Unregister(t.Context()); err != nil {
		t.Fatalf("Unregister() error = %v", err)
	}
	if _, err := os.Stat(manager.DefinitionPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("definition stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file was removed: %v", err)
	}
	assertCommandCalls(t, runner.calls, []commandCall{{name: "systemctl", args: []string{"--user", "daemon-reload"}}})
}

func TestParseSystemdStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  NativeStatus
	}{
		{name: "running", input: "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=1234\n", want: NativeStatus{Registered: true, State: NativeRunning, PID: 1234}},
		{name: "not found", input: "LoadState=not-found\nActiveState=inactive\nSubState=dead\nMainPID=0\n", want: NativeStatus{State: NativeStopped}},
		{name: "inactive", input: "LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\n", want: NativeStatus{Registered: true, State: NativeStopped}},
		{name: "failed", input: "LoadState=loaded\nActiveState=failed\nSubState=failed\nMainPID=0\n", want: NativeStatus{Registered: true, State: NativeFailed}},
		{name: "malformed", input: "garbage\n", want: NativeStatus{State: NativeFailed}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseSystemdStatus([]byte(test.input))
			if err != nil {
				t.Fatalf("parseSystemdStatus() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseSystemdStatus() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSystemdStatusCommandFailureProjectsFailed(t *testing.T) {
	t.Parallel()

	runner := &fakeCommandRunner{run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("user bus unavailable")
	}}
	manager, err := newSystemdManager(t.TempDir(), runner)
	if err != nil {
		t.Fatalf("newSystemdManager() error = %v", err)
	}
	status, err := manager.Status(t.Context())
	if err == nil || !strings.Contains(err.Error(), "systemctl status") {
		t.Fatalf("Status() error = %v, want projected status error", err)
	}
	if status.State != NativeFailed {
		t.Fatalf("Status() = %#v, want failed", status)
	}
}

func parseSystemdExecStart(t *testing.T, unit string) []string {
	t.Helper()
	line := findUnitLine(t, unit, "ExecStart=")
	rest := strings.TrimPrefix(line, "ExecStart=")
	var values []string
	for strings.TrimSpace(rest) != "" {
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "\"") {
			t.Fatalf("ExecStart token is not quoted: %q", rest)
		}
		end := -1
		for index := 1; index < len(rest); index++ {
			if rest[index] == '"' && !isEscaped(rest, index) {
				end = index
				break
			}
		}
		if end == -1 {
			t.Fatalf("unterminated ExecStart token: %q", rest)
		}
		value, err := strconv.Unquote(rest[:end+1])
		if err != nil {
			t.Fatalf("unquote ExecStart token: %v", err)
		}
		values = append(values, strings.ReplaceAll(value, "%%", "%"))
		rest = rest[end+1:]
	}
	return values
}

func isEscaped(value string, index int) bool {
	backslashes := 0
	for position := index - 1; position >= 0 && value[position] == '\\'; position-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func parseSystemdEnvironment(t *testing.T, unit string) map[string]string {
	t.Helper()
	values := make(map[string]string)
	for line := range strings.SplitSeq(unit, "\n") {
		if !strings.HasPrefix(line, "Environment=") {
			continue
		}
		value, err := strconv.Unquote(strings.TrimPrefix(line, "Environment="))
		if err != nil {
			t.Fatalf("unquote environment: %v", err)
		}
		name, environmentValue, ok := strings.Cut(value, "=")
		if !ok {
			t.Fatalf("environment line has no separator: %q", value)
		}
		values[name] = strings.ReplaceAll(environmentValue, "%%", "%")
	}
	return values
}

func findUnitLine(t *testing.T, unit, prefix string) string {
	t.Helper()
	for line := range strings.SplitSeq(unit, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("unit has no %q line: %s", prefix, unit)
	return ""
}
