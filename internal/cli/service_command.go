package cli

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
)

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type commandRunner struct {
	command func(context.Context, string, ...string) *exec.Cmd
}

func NewCommandRunner(command func(context.Context, string, ...string) *exec.Cmd) CommandRunner {
	if command == nil {
		command = exec.CommandContext
	}
	return commandRunner{command: command}
}
func (r commandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.command(ctx, name, args...).CombinedOutput()
}
func NewNativeServiceManager(goos, home string, uid int, runner CommandRunner) (ServiceManager, error) {
	switch goos {
	case "darwin":
		return newLaunchdManager(home, uid, runner)
	case "linux":
		return newSystemdManager(home, runner)
	default:
		return nil, errors.New("native services are unsupported; use dataporch run -f")
	}
}
func newNativeServiceManager(home string, uid int, runner CommandRunner) (ServiceManager, error) {
	return NewNativeServiceManager(runtime.GOOS, home, uid, runner)
}
