package cli

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type commandCall struct {
	name string
	args []string
}

type serviceCommandContextKey struct{}

type fakeCommandRunner struct {
	calls []commandCall
	run   func(context.Context, string, ...string) ([]byte, error)
}

func (runner *fakeCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, commandCall{name: name, args: append([]string(nil), args...)})
	if runner.run != nil {
		return runner.run(ctx, name, args...)
	}
	return nil, nil
}

func TestRenderLaunchdPlistPreservesArgumentsAndData(t *testing.T) {
	t.Parallel()

	definition := ServiceDefinition{
		Executable:  `/Users/Alice/Data Porch/$()/dataporch%;"`,
		Arguments:   []string{"run", "-f"},
		Environment: []EnvironmentVariable{{Name: "DATAPORCH_HTTP_ADDRESS", Value: `127.0.0.1:8080;$(echo bad)&<>%"`}},
		StdoutPath:  `/Users/Alice/Data Porch/logs/out&<>%.log`,
		StderrPath:  `/Users/Alice/Data Porch/logs/err"%.log`,
	}

	data, err := renderLaunchdPlist(definition)
	if err != nil {
		t.Fatalf("renderLaunchdPlist() error = %v", err)
	}
	args, environment, paths := decodeLaunchdPlist(t, data)
	if want := []string{definition.Executable, "run", "-f"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("ProgramArguments = %#v, want %#v", args, want)
	}
	if got := environment["DATAPORCH_HTTP_ADDRESS"]; got != definition.Environment[0].Value {
		t.Fatalf("environment value = %q, want %q", got, definition.Environment[0].Value)
	}
	if paths["StandardOutPath"] != definition.StdoutPath || paths["StandardErrorPath"] != definition.StderrPath {
		t.Fatalf("log paths = %#v, want stdout/stderr paths", paths)
	}
	text := string(data)
	for _, forbidden := range []string{"/bin/sh", " -c", "RunAtLoad", "KeepAlive", "secret-canary", "unrelated-canary"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("plist contains forbidden value %q: %s", forbidden, text)
		}
	}
}

func TestRenderLaunchdPlistRejectsControlCharacters(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"/bin/data\nporch", "/bin/data\x00porch"} {
		if _, err := renderLaunchdPlist(ServiceDefinition{Executable: value}); err == nil {
			t.Fatalf("renderLaunchdPlist(%q) error = nil", value)
		}
	}
}

func TestLaunchdManagerUsesExplicitCommands(t *testing.T) {
	t.Parallel()

	home := "/Users/alice"
	runner := &fakeCommandRunner{}
	manager, err := newLaunchdManager(home, 501, runner)
	if err != nil {
		t.Fatalf("newLaunchdManager() error = %v", err)
	}
	ctx := context.Background()

	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	assertCommandCalls(t, runner.calls, []commandCall{
		{name: "launchctl", args: []string{"bootstrap", "gui/501", "/Users/alice/Library/LaunchAgents/com.dataporch.dataporch.plist"}},
		{name: "launchctl", args: []string{"kickstart", "gui/501/com.dataporch.dataporch"}},
	})

	runner.calls = nil
	if err := manager.Restart(ctx); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	assertCommandCalls(t, runner.calls, []commandCall{
		{name: "launchctl", args: []string{"bootout", "gui/501/com.dataporch.dataporch"}},
		{name: "launchctl", args: []string{"bootstrap", "gui/501", "/Users/alice/Library/LaunchAgents/com.dataporch.dataporch.plist"}},
		{name: "launchctl", args: []string{"kickstart", "gui/501/com.dataporch.dataporch"}},
	})

	runner.calls = nil
	if err := manager.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	assertCommandCalls(t, runner.calls, []commandCall{{name: "launchctl", args: []string{"bootout", "gui/501/com.dataporch.dataporch"}}})
}

func TestLaunchdManagerTreatsAbsentServiceAsSuccess(t *testing.T) {
	t.Parallel()

	callCount := 0
	runner := &fakeCommandRunner{run: func(context.Context, string, ...string) ([]byte, error) {
		callCount++
		if callCount == 1 || callCount == 2 {
			return []byte("Could not find service"), errors.New("exit status 3")
		}
		return nil, nil
	}}
	manager, err := newLaunchdManager(t.TempDir(), 501, runner)
	if err != nil {
		t.Fatalf("newLaunchdManager() error = %v", err)
	}
	if err := manager.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v, want nil for absent service", err)
	}
	if err := manager.Restart(t.Context()); err != nil {
		t.Fatalf("Restart() error = %v, want absent bootout to be ignored", err)
	}
}

func TestLaunchdManagerRegistersAtomicallyAndUnregistersOnlyDefinition(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runner := &fakeCommandRunner{}
	manager, err := newLaunchdManager(home, 501, runner)
	if err != nil {
		t.Fatalf("newLaunchdManager() error = %v", err)
	}
	definition := ServiceDefinition{Executable: "/opt/dataporch", Arguments: []string{"run", "-f"}, StdoutPath: filepath.Join(home, ".dataporch", "logs", "out.log"), StderrPath: filepath.Join(home, ".dataporch", "logs", "err.log")}
	if err := manager.Register(t.Context(), definition); err != nil {
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
	if err := os.WriteFile(statePath, []byte("state"), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := manager.Unregister(t.Context()); err != nil {
		t.Fatalf("Unregister() error = %v", err)
	}
	if _, err := os.Stat(manager.DefinitionPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("definition stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file was removed: %v", err)
	}
}

func TestParseLaunchdStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  NativeStatus
	}{
		{name: "running", input: "state = running\npid = 1234\n", want: NativeStatus{Registered: true, State: NativeRunning, PID: 1234}},
		{name: "stopped", input: "state = exited\n", want: NativeStatus{Registered: true, State: NativeStopped}},
		{name: "failed", input: "state = crashed\n", want: NativeStatus{Registered: true, State: NativeFailed}},
		{name: "malformed", input: "garbage\n", want: NativeStatus{Registered: true, State: NativeFailed}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseLaunchdStatus([]byte(test.input))
			if err != nil {
				t.Fatalf("parseLaunchdStatus() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseLaunchdStatus() = %#v, want %#v", got, test.want)
			}
		})
	}
}

//nolint:gocyclo // Test-only XML token parsing keeps the plist assertions close to the format it verifies.
func decodeLaunchdPlist(t *testing.T, data []byte) ([]string, map[string]string, map[string]string) {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	var stack []string
	var pendingKey string
	var args []string
	environment := map[string]string{}
	paths := map[string]string{}
	insideArguments := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode plist: %v", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			stack = append(stack, value.Name.Local)
			if value.Name.Local == "array" && pendingKey == "ProgramArguments" {
				insideArguments = true
				pendingKey = ""
			}
			if value.Name.Local == "key" {
				var key string
				if err := decoder.DecodeElement(&key, &value); err != nil {
					t.Fatalf("decode plist key: %v", err)
				}
				pendingKey = key
				stack = stack[:len(stack)-1]
			}
			if value.Name.Local == "string" {
				var stringValue string
				if err := decoder.DecodeElement(&stringValue, &value); err != nil {
					t.Fatalf("decode plist string: %v", err)
				}
				switch {
				case insideArguments:
					args = append(args, stringValue)
				case pendingKey == "StandardOutPath" || pendingKey == "StandardErrorPath":
					paths[pendingKey] = stringValue
				default:
					if pendingKey != "" {
						environment[pendingKey] = stringValue
					}
				}
				pendingKey = ""
				stack = stack[:len(stack)-1]
			}
		case xml.EndElement:
			if value.Name.Local == "array" && insideArguments {
				insideArguments = false
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return args, environment, paths
}

func assertCommandCalls(t *testing.T, got, want []commandCall) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("command calls = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index].name != want[index].name || !reflect.DeepEqual(got[index].args, want[index].args) {
			t.Fatalf("command call %d = %s %#v, want %s %#v", index, got[index].name, got[index].args, want[index].name, want[index].args)
		}
	}
}
