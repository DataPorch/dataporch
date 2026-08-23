package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/adamraziv/dataporch/internal/atomicfile"
)

const systemdUnit = "dataporch.service"

type systemdManager struct {
	home   string
	runner CommandRunner
}

func newSystemdManager(home string, runner CommandRunner) (*systemdManager, error) {
	if home == "" || runner == nil {
		return nil, errors.New("systemd manager requires home and command runner")
	}
	return &systemdManager{home: home, runner: runner}, nil
}
func (m *systemdManager) DefinitionPath() string {
	return filepath.Join(m.home, ".config", "systemd", "user", systemdUnit)
}
func (m *systemdManager) LogLocation() string { return "journalctl --user-unit " + systemdUnit }
func (m *systemdManager) command(ctx context.Context, args ...string) ([]byte, error) {
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return m.runner.Run(child, "systemctl", append([]string{"--user"}, args...)...)
}
func (m *systemdManager) Register(ctx context.Context, definition ServiceDefinition) error {
	if err := validateDefinition(definition); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.DefinitionPath()), 0o700); err != nil {
		return err
	}
	data, err := renderSystemdUnit(definition)
	if err != nil {
		return err
	}
	if err := atomicfile.Replace(m.DefinitionPath(), data, 0o600); err != nil {
		return err
	}
	_, err = m.command(ctx, "daemon-reload")
	return err
}
func (m *systemdManager) Start(ctx context.Context) error {
	_, err := m.command(ctx, "start", systemdUnit)
	return err
}
func (m *systemdManager) Restart(ctx context.Context) error {
	if _, err := m.command(ctx, "daemon-reload"); err != nil {
		return err
	}
	_, err := m.command(ctx, "restart", systemdUnit)
	return err
}
func (m *systemdManager) Stop(ctx context.Context) error {
	_, err := m.command(ctx, "stop", systemdUnit)
	return err
}
func (m *systemdManager) Unregister(ctx context.Context) error {
	if err := os.Remove(m.DefinitionPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, err := m.command(ctx, "daemon-reload")
	return err
}
func (m *systemdManager) Status(ctx context.Context) (NativeStatus, error) {
	output, err := m.command(ctx, "show", systemdUnit, "--property=LoadState", "--property=ActiveState", "--property=SubState", "--property=MainPID")
	if err != nil {
		return NativeStatus{}, err
	}
	return parseSystemdStatus(output)
}
func renderSystemdUnit(definition ServiceDefinition) ([]byte, error) {
	if err := validateDefinition(definition); err != nil {
		return nil, err
	}
	quote := func(value string) (string, error) {
		if strings.ContainsAny(value, "\x00\n") {
			return "", errors.New("systemd value contains NUL or newline")
		}
		return strconv.Quote(strings.ReplaceAll(value, "%", "%%")), nil
	}
	parts := []string{}
	for _, arg := range append([]string{definition.Executable}, definition.Arguments...) {
		value, err := quote(arg)
		if err != nil {
			return nil, err
		}
		parts = append(parts, value)
	}
	lines := []string{"[Service]", "Type=simple", "ExecStart=" + strings.Join(parts, " "), "Restart=no"}
	for _, variable := range definition.Environment {
		value, err := quote(variable.Value)
		if err != nil {
			return nil, err
		}
		lines = append(lines, "Environment=\""+strings.ReplaceAll(variable.Name, "%", "%%")+"="+strings.Trim(value, "\"")+"\"")
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}
func parseSystemdStatus(output []byte) (NativeStatus, error) {
	values := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	if values["LoadState"] == "not-found" || values["ActiveState"] == "inactive" {
		return NativeStatus{Registered: values["LoadState"] == "loaded", State: NativeStopped}, nil
	}
	if values["LoadState"] != "loaded" || values["ActiveState"] != "active" || values["SubState"] != "running" {
		return NativeStatus{Registered: values["LoadState"] == "loaded", State: NativeFailed}, nil
	}
	pid, err := strconv.Atoi(values["MainPID"])
	if err != nil || pid <= 0 {
		return NativeStatus{Registered: true, State: NativeFailed}, nil
	}
	return NativeStatus{Registered: true, State: NativeRunning, PID: pid}, nil
}
