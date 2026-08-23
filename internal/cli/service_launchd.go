package cli

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adamraziv/dataporch/internal/atomicfile"
)

const launchdLabel = "com.dataporch.dataporch"

type launchdManager struct {
	home   string
	uid    int
	runner CommandRunner
}

func newLaunchdManager(home string, uid int, runner CommandRunner) (*launchdManager, error) {
	if home == "" || runner == nil {
		return nil, errors.New("launchd manager requires home and command runner")
	}
	return &launchdManager{home: home, uid: uid, runner: runner}, nil
}
func (m *launchdManager) DefinitionPath() string {
	return filepath.Join(m.home, "Library", "LaunchAgents", launchdLabel+".plist")
}
func (m *launchdManager) LogLocation() string { return filepath.Join(m.home, ".dataporch", "logs") }
func (m *launchdManager) domain() string      { return fmt.Sprintf("gui/%d", m.uid) }
func (m *launchdManager) target() string      { return m.domain() + "/" + launchdLabel }
func (m *launchdManager) Register(ctx context.Context, definition ServiceDefinition) error {
	if err := validateDefinition(definition); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.DefinitionPath()), 0o700); err != nil {
		return fmt.Errorf("creating launch agents directory: %w", err)
	}
	if err := os.MkdirAll(m.LogLocation(), 0o700); err != nil {
		return fmt.Errorf("creating log directory: %w", err)
	}
	data, err := renderLaunchdPlist(definition)
	if err != nil {
		return err
	}
	if err := atomicfile.Replace(m.DefinitionPath(), data, 0o600); err != nil {
		return fmt.Errorf("writing launchd definition: %w", err)
	}
	return nil
}
func (m *launchdManager) command(ctx context.Context, args ...string) ([]byte, error) {
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return m.runner.Run(child, "launchctl", args...)
}
func (m *launchdManager) Start(ctx context.Context) error {
	if _, err := m.command(ctx, "bootstrap", m.domain(), m.DefinitionPath()); err != nil {
		return err
	}
	_, err := m.command(ctx, "kickstart", m.target())
	return err
}
func (m *launchdManager) Restart(ctx context.Context) error {
	_, _ = m.command(ctx, "bootout", m.target())
	return m.Start(ctx)
}
func (m *launchdManager) Stop(ctx context.Context) error {
	_, err := m.command(ctx, "bootout", m.target())
	return err
}
func (m *launchdManager) Unregister(ctx context.Context) error {
	if err := os.Remove(m.DefinitionPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
func (m *launchdManager) Status(ctx context.Context) (NativeStatus, error) {
	output, err := m.command(ctx, "print", m.target())
	if err != nil {
		return NativeStatus{Registered: false, State: NativeStopped}, nil
	}
	return parseLaunchdStatus(output)
}

type plist struct {
	XMLName xml.Name  `xml:"plist"`
	Version string    `xml:"version,attr"`
	Dict    plistDict `xml:"dict"`
}
type plistDict struct {
	Keys    []string     `xml:"key"`
	Strings []string     `xml:"string"`
	Arrays  []plistArray `xml:"array"`
	Dicts   []plistDict  `xml:"dict"`
}
type plistArray struct {
	Strings []string `xml:"string"`
}

func renderLaunchdPlist(definition ServiceDefinition) ([]byte, error) {
	if err := validateDefinition(definition); err != nil {
		return nil, err
	}
	var body bytes.Buffer
	body.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>")
	writeKeyString := func(key, value string) {
		fmt.Fprintf(&body, "<key>%s</key><string>%s</string>", xmlEscape(key), xmlEscape(value))
	}
	writeKeyString("Label", launchdLabel)
	body.WriteString("<key>ProgramArguments</key><array>")
	for _, argument := range append([]string{definition.Executable}, definition.Arguments...) {
		fmt.Fprintf(&body, "<string>%s</string>", xmlEscape(argument))
	}
	body.WriteString("</array><key>EnvironmentVariables</key><dict>")
	for _, variable := range definition.Environment {
		writeKeyString(variable.Name, variable.Value)
	}
	body.WriteString("</dict>")
	writeKeyString("StandardOutPath", definition.StdoutPath)
	writeKeyString("StandardErrorPath", definition.StderrPath)
	body.WriteString("</dict></plist>")
	return body.Bytes(), nil
}
func xmlEscape(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}
func parseLaunchdStatus(output []byte) (NativeStatus, error) {
	text := string(output)
	if strings.Contains(text, "state = running") {
		status := NativeStatus{Registered: true, State: NativeRunning}
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "pid =") {
				fmt.Sscanf(line, " pid = %d", &status.PID)
			}
		}
		return status, nil
	}
	if strings.Contains(text, "state =") {
		return NativeStatus{Registered: true, State: NativeStopped}, nil
	}
	return NativeStatus{Registered: true, State: NativeFailed}, nil
}
