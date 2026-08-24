//go:build integration

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const lifecycleTimeout = 45 * time.Second

//nolint:funlen,gocyclo,paralleltest // The native service fixture must be serialized to protect the per-user manager namespace.
func TestInstalledBinaryNativeLifecycle(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Fatalf("unsupported acceptance host %q", runtime.GOOS)
	}
	refuseExistingLaunchdService(t)

	root := resolvedTempDir(t)
	stateRoot := filepath.Join(root, "state $();%;nested", "state leaf")
	binRoot := filepath.Join(root, "bin $();%;nested", "bin leaf")
	homeRoot := filepath.Join(root, "home")
	for _, directory := range []string{stateRoot, binRoot, homeRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create acceptance directory %q: %v", directory, err)
		}
	}

	installCurrentBinary(t, binRoot)
	binaryPath := filepath.Join(binRoot, "dataporch")
	if _, err := os.Stat(binaryPath); err != nil {
		t.Fatalf("installed binary stat: %v", err)
	}

	address := freeTCPAddress(t)
	environment := lifecycleEnvironment(t, homeRoot, stateRoot, address)
	definitionPath := nativeDefinitionPath(homeRoot)
	linkSystemdDefinition(t, definitionPath)
	serviceStarted := false
	stopBinary := func() {
		if !serviceStarted {
			return
		}
		_, _, _ = invokeBinary(t, binaryPath, environment, "stop")
	}
	t.Cleanup(stopBinary)

	stdout, stderr, code := invokeBinary(t, binaryPath, environment, "secrets", "init")
	if code != 0 {
		t.Fatalf("secrets init exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stateFiles := []string{
		environmentValue(environment, "DATAPORCH_MASTER_KEY_PATH"),
		environmentValue(environment, "DATAPORCH_SECRETS_STORE_PATH"),
	}
	for _, path := range stateFiles {
		assertOwnerOnlyFile(t, path)
	}

	stdout, stderr, code = invokeBinary(t, binaryPath, environment, "run")
	if code != 0 {
		t.Fatalf("run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	serviceStarted = true
	if _, err := os.Stat(definitionPath); err != nil {
		t.Fatalf("native definition stat: %v", err)
	}
	definition, err := os.ReadFile(definitionPath)
	if err != nil {
		t.Fatalf("read native definition: %v", err)
	}
	definitionText := string(definition)
	definitionExecutable := binaryPath
	definitionStateRoot := stateRoot
	if runtime.GOOS == "linux" {
		definitionExecutable = strings.ReplaceAll(definitionExecutable, "%", "%%")
		definitionStateRoot = strings.ReplaceAll(definitionStateRoot, "%", "%%")
	}
	if !strings.Contains(definitionText, definitionExecutable) || !strings.Contains(definitionText, definitionStateRoot) {
		t.Fatalf("native definition lost executable or state path: %s", definitionText)
	}
	if strings.Contains(definitionText, "DATAPORCH_MCP_TOKEN=") || strings.Contains(definitionText, "UNRELATED_ENV") {
		t.Fatalf("native definition captured client or unrelated environment: %s", definitionText)
	}
	if runtime.GOOS == "darwin" && (strings.Contains(definitionText, "RunAtLoad") || strings.Contains(definitionText, "KeepAlive")) {
		t.Fatalf("launchd definition enables login startup: %s", definitionText)
	}
	if runtime.GOOS == "linux" {
		assertSystemdUnitIsNotEnabled(t, definitionPath, environment)
	}

	stdout, stderr, code = invokeBinary(t, binaryPath, environment, "run")
	if code != 0 {
		t.Fatalf("idempotent run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	oldPID := statusPID(t, stdout, code, binaryPath, environment)

	stdout, stderr, code = invokeBinary(t, binaryPath, environment, "status")
	if code != 0 || !strings.Contains(stdout, "running") || !strings.Contains(stdout, "logs:") {
		t.Fatalf("status before restart exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if pid := parsePID(stdout); pid != oldPID {
		t.Fatalf("status pid = %d, want %d", pid, oldPID)
	}

	stdout, stderr, code = invokeBinary(t, binaryPath, environment, "restart")
	if code != 0 {
		t.Fatalf("restart exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = invokeBinary(t, binaryPath, environment, "status")
	if code != 0 || !strings.Contains(stdout, "running") {
		t.Fatalf("status after restart exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if pid := parsePID(stdout); pid == oldPID {
		t.Fatalf("restart kept PID %d", pid)
	}

	stdout, stderr, code = invokeBinary(t, binaryPath, environment, "stop")
	if code != 0 {
		t.Fatalf("stop exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	serviceStarted = false
	if _, err := os.Stat(definitionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("definition after stop error=%v, want not exist", err)
	}
	for _, path := range stateFiles {
		assertOwnerOnlyFile(t, path)
	}

	stdout, stderr, code = invokeBinary(t, binaryPath, environment, "status")
	if code != 3 || !strings.Contains(stdout, "stopped") {
		t.Fatalf("stopped status exit=%d stdout=%q stderr=%q, want exit 3", code, stdout, stderr)
	}
	stdout, stderr, code = invokeBinary(t, binaryPath, environment, "stop")
	if code != 0 {
		t.Fatalf("idempotent stop exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	foregroundContext, cancel := context.WithTimeout(t.Context(), lifecycleTimeout)
	defer cancel()
	foreground := exec.CommandContext(foregroundContext, binaryPath, "run", "-f")
	foreground.Env = environment
	foregroundStdout := &bytes.Buffer{}
	foregroundStderr := &bytes.Buffer{}
	foreground.Stdout = foregroundStdout
	foreground.Stderr = foregroundStderr
	if err := foreground.Start(); err != nil {
		t.Fatalf("foreground start: %v", err)
	}
	if err := waitForHealth(foregroundContext, address); err != nil {
		_ = foreground.Process.Kill()
		_ = foreground.Wait()
		t.Fatalf("foreground health: %v; stdout=%q stderr=%q", err, foregroundStdout.String(), foregroundStderr.String())
	}
	if err := foreground.Process.Signal(os.Interrupt); err != nil {
		_ = foreground.Process.Kill()
		t.Fatalf("foreground interrupt: %v", err)
	}
	if err := waitCommand(foreground, lifecycleTimeout); err != nil {
		t.Fatalf("foreground wait after interrupt: %v; stdout=%q stderr=%q", err, foregroundStdout.String(), foregroundStderr.String())
	}
}

func installCurrentBinary(t *testing.T, binRoot string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", "install", "-trimpath", "./cmd/dataporch")
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	moduleRoot, err := filepath.Abs(filepath.Join(workingDirectory, "../.."))
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	command.Dir = moduleRoot
	command.Env = append(filteredEnvironment(os.Environ(), "GOBIN="), "GOBIN="+binRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go install error = %v\n%s", err, output)
	}
}

func lifecycleEnvironment(t *testing.T, home, stateRoot, address string) []string {
	t.Helper()
	return append(filteredEnvironment(os.Environ(), "HOME=", "DATAPORCH_", "UNRELATED_ENV="),
		"HOME="+home,
		"DATAPORCH_HTTP_ADDRESS="+address,
		"DATAPORCH_ADMIN_SOCKET_PATH="+filepath.Join(stateRoot, "admin.sock"),
		"DATAPORCH_MASTER_KEY_PATH="+filepath.Join(stateRoot, "master.key"),
		"DATAPORCH_SECRETS_STORE_PATH="+filepath.Join(stateRoot, "secrets.store"),
		"DATAPORCH_CONNECTIONS_STORE_PATH="+filepath.Join(stateRoot, "connections.store"),
		"DATAPORCH_MCP_TOKEN_STORE_PATH="+filepath.Join(stateRoot, "mcp-token.json"),
		"DATAPORCH_MCP_TOKEN=secret-canary",
		"UNRELATED_ENV=unrelated-canary",
	)
}

func filteredEnvironment(environment []string, prefixes ...string) []string {
	filtered := make([]string, 0, len(environment))
	for _, value := range environment {
		remove := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(value, prefix) {
				remove = true
				break
			}
		}
		if !remove {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func invokeBinary(t *testing.T, binary string, environment []string, args ...string) (string, string, int) {
	t.Helper()
	commandContext, cancel := context.WithTimeout(context.Background(), lifecycleTimeout)
	defer cancel()
	command := exec.CommandContext(commandContext, binary, args...)
	command.Env = environment
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return stdout.String(), stderr.String(), exitError.ExitCode()
	}
	t.Fatalf("run %q: %v", append([]string{binary}, args...), err)
	return "", "", -1
}

func statusPID(t *testing.T, stdout string, code int, binary string, environment []string) int {
	t.Helper()
	if code != 0 || !strings.Contains(stdout, "running") {
		statusStdout, statusStderr, statusCode := invokeBinary(t, binary, environment, "status")
		if statusCode != 0 {
			t.Fatalf("status exit=%d stdout=%q stderr=%q", statusCode, statusStdout, statusStderr)
		}
		stdout = statusStdout
	}
	return parsePID(stdout)
}

func parsePID(output string) int {
	for line := range strings.SplitSeq(output, "\n") {
		if value, ok := strings.CutPrefix(line, "pid: "); ok {
			pid, _ := strconv.Atoi(strings.TrimSpace(value))
			return pid
		}
	}
	return 0
}

func nativeDefinitionPath(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "LaunchAgents", "com.dataporch.dataporch.plist")
	}
	return filepath.Join(home, ".config", "systemd", "user", "dataporch.service")
}

func refuseExistingLaunchdService(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}

	target := fmt.Sprintf("gui/%d/com.dataporch.dataporch", os.Getuid())
	command := exec.CommandContext(t.Context(), "launchctl", "print", target)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("launchd service %q is already loaded; stop it before running the lifecycle acceptance test", target)
	}
	if !strings.Contains(strings.ToLower(string(output)), "could not find service") {
		t.Fatalf("inspect launchd service %q: %v: %s", target, err, output)
	}
}

func linkSystemdDefinition(t *testing.T, definitionPath string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return
	}

	managerHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve systemd manager home: %v", err)
	}
	managerDefinitionPath := filepath.Join(managerHome, ".config", "systemd", "user", filepath.Base(definitionPath))
	if _, err := os.Lstat(managerDefinitionPath); err == nil {
		t.Fatalf("systemd unit path already exists: %q", managerDefinitionPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect systemd unit path %q: %v", managerDefinitionPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(managerDefinitionPath), 0o700); err != nil {
		t.Fatalf("create systemd unit directory: %v", err)
	}
	if err := os.Symlink(definitionPath, managerDefinitionPath); err != nil {
		t.Fatalf("link systemd unit: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(managerDefinitionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove systemd unit link %q: %v", managerDefinitionPath, err)
		}
	})
}

func assertSystemdUnitIsNotEnabled(t *testing.T, definitionPath string, environment []string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "systemctl", "--user", "is-enabled", filepath.Base(definitionPath))
	command.Env = environment
	output, err := command.CombinedOutput()
	if err == nil && strings.TrimSpace(string(output)) == "enabled" {
		t.Fatalf("systemd unit is enabled: %q", output)
	}
}

func assertOwnerOnlyFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat state file %q: %v", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("state file %q mode = %o, want owner-only", path, info.Mode().Perm())
	}
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, value := range environment {
		if value, ok := strings.CutPrefix(value, prefix); ok {
			return value
		}
	}
	return ""
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close free port listener: %v", err)
	}
	return address
}

func waitForHealth(ctx context.Context, address string) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/healthz", nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				body, readErr := io.ReadAll(response.Body)
				closeErr := response.Body.Close()
				if readErr == nil && closeErr == nil && response.StatusCode == http.StatusOK && strings.Contains(string(body), `"status":"ok"`) {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func waitCommand(command *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = command.Process.Kill()
		return fmt.Errorf("command did not exit within %s", timeout)
	}
}

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return resolved
}
