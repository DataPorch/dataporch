package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestNewNativeServiceManagerSelectsSupportedPlatform(t *testing.T) {
	t.Parallel()

	runner := &fakeCommandRunner{}
	for _, test := range []struct {
		name string
		goos string
		want string
	}{
		{name: "macos", goos: "darwin", want: "*cli.launchdManager"},
		{name: "linux", goos: "linux", want: "*cli.systemdManager"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager, err := NewNativeServiceManager(test.goos, t.TempDir(), 501, runner)
			if err != nil {
				t.Fatalf("NewNativeServiceManager() error = %v", err)
			}
			if got := fmt.Sprintf("%T", manager); got != test.want {
				t.Fatalf("manager type = %s, want %s", got, test.want)
			}
		})
	}
}

func TestNewNativeServiceManagerRejectsUnsupportedPlatform(t *testing.T) {
	t.Parallel()

	manager, err := NewNativeServiceManager("windows", t.TempDir(), 501, &fakeCommandRunner{})
	if manager != nil {
		t.Fatalf("manager = %T, want nil", manager)
	}
	if err == nil || !strings.Contains(err.Error(), "use dataporch run -f") {
		t.Fatalf("error = %v, want foreground recommendation", err)
	}
}

func TestCommandRunnerPreservesContextAndArgumentBoundaries(t *testing.T) {
	t.Parallel()

	called := false
	ctx := context.WithValue(t.Context(), serviceCommandContextKey{}, "value")
	runner := NewCommandRunner(func(got context.Context, name string, args ...string) *exec.Cmd {
		called = true
		return exec.CommandContext(got, name, args...)
	})
	_, _ = runner.Run(ctx, "printf", "space value", "$(literal)")
	if !called {
		t.Fatal("command constructor was not called")
	}
}
