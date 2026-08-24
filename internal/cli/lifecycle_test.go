package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/adamraziv/dataporch/internal/config"
)

type recordingServiceManager struct {
	calls          []string
	status         NativeStatus
	statusErr      error
	registerErr    error
	startErr       error
	restartErr     error
	stopErr        error
	unregisterErr  error
	definitionPath string
	logLocation    string
	definition     ServiceDefinition
	contexts       []context.Context
}

type lifecycleContextKey struct{}

func (manager *recordingServiceManager) record(name string, ctx context.Context) {
	manager.calls = append(manager.calls, name)
	manager.contexts = append(manager.contexts, ctx)
}

func (manager *recordingServiceManager) Register(ctx context.Context, definition ServiceDefinition) error {
	manager.record("Register", ctx)
	manager.definition = definition
	return manager.registerErr
}

func (manager *recordingServiceManager) Start(ctx context.Context) error {
	manager.record("Start", ctx)
	return manager.startErr
}

func (manager *recordingServiceManager) Restart(ctx context.Context) error {
	manager.record("Restart", ctx)
	return manager.restartErr
}

func (manager *recordingServiceManager) Stop(ctx context.Context) error {
	manager.record("Stop", ctx)
	return manager.stopErr
}

func (manager *recordingServiceManager) Unregister(ctx context.Context) error {
	manager.record("Unregister", ctx)
	return manager.unregisterErr
}

func (manager *recordingServiceManager) Status(ctx context.Context) (NativeStatus, error) {
	manager.record("Status", ctx)
	return manager.status, manager.statusErr
}
func (manager *recordingServiceManager) DefinitionPath() string { return manager.definitionPath }
func (manager *recordingServiceManager) LogLocation() string    { return manager.logLocation }

type recordingHealthChecker struct {
	calls     []string
	checkErr  error
	waitErr   error
	contexts  []context.Context
	addresses []string
}

func (checker *recordingHealthChecker) Check(ctx context.Context, address string) error {
	checker.calls = append(checker.calls, "Check")
	checker.contexts = append(checker.contexts, ctx)
	checker.addresses = append(checker.addresses, address)
	return checker.checkErr
}

func (checker *recordingHealthChecker) Wait(ctx context.Context, address string) error {
	checker.calls = append(checker.calls, "Wait")
	checker.contexts = append(checker.contexts, ctx)
	checker.addresses = append(checker.addresses, address)
	return checker.waitErr
}

func TestRunBackgroundLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      NativeStatus
		wantManager []string
		wantHealth  []string
	}{
		{name: "stopped", status: NativeStatus{State: NativeStopped}, wantManager: []string{"Status", "Register", "Start"}, wantHealth: []string{"Wait"}},
		{name: "registered stopped", status: NativeStatus{Registered: true, State: NativeStopped}, wantManager: []string{"Status", "Register", "Restart"}, wantHealth: []string{"Wait"}},
		{name: "healthy", status: NativeStatus{Registered: true, State: NativeRunning}, wantManager: []string{"Status"}, wantHealth: []string{"Check"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager := &recordingServiceManager{status: test.status}
			health := &recordingHealthChecker{}
			runner := lifecycleTestRunner(t, manager, health)

			if err := runner.runBackground(t.Context()); err != nil {
				t.Fatalf("runBackground() error = %v", err)
			}
			if !reflect.DeepEqual(manager.calls, test.wantManager) {
				t.Fatalf("manager calls = %#v, want %#v", manager.calls, test.wantManager)
			}
			if !reflect.DeepEqual(health.calls, test.wantHealth) {
				t.Fatalf("health calls = %#v, want %#v", health.calls, test.wantHealth)
			}
			if manager.definition.Arguments == nil && test.name == "stopped" {
				t.Fatal("stopped run did not build a service definition")
			}
		})
	}
}

func TestRestartBackgroundRefreshesRegisteredService(t *testing.T) {
	t.Parallel()

	manager := &recordingServiceManager{status: NativeStatus{Registered: true, State: NativeStopped}}
	health := &recordingHealthChecker{}
	runner := lifecycleTestRunner(t, manager, health)

	if err := runner.restartBackground(t.Context()); err != nil {
		t.Fatalf("restartBackground() error = %v", err)
	}
	if want := []string{"Status", "Register", "Restart"}; !reflect.DeepEqual(manager.calls, want) {
		t.Fatalf("manager calls = %#v, want %#v", manager.calls, want)
	}
	if want := []string{"Wait"}; !reflect.DeepEqual(health.calls, want) {
		t.Fatalf("health calls = %#v, want %#v", health.calls, want)
	}
}

func TestRestartBackgroundRequiresRegisteredService(t *testing.T) {
	t.Parallel()

	manager := &recordingServiceManager{status: NativeStatus{State: NativeStopped}}
	runner := lifecycleTestRunner(t, manager, &recordingHealthChecker{})

	err := runner.restartBackground(t.Context())
	if err == nil || !strings.Contains(err.Error(), "run dataporch run") {
		t.Fatalf("restartBackground() error = %v, want run instruction", err)
	}
	if !reflect.DeepEqual(manager.calls, []string{"Status"}) {
		t.Fatalf("manager calls = %#v, want [Status]", manager.calls)
	}
}

func TestStopBackgroundIsIdempotent(t *testing.T) {
	t.Parallel()

	manager := &recordingServiceManager{status: NativeStatus{State: NativeStopped}}
	runner := lifecycleTestRunner(t, manager, &recordingHealthChecker{})

	if err := runner.stopBackground(t.Context()); err != nil {
		t.Fatalf("stopBackground() error = %v", err)
	}
	if want := []string{"Stop", "Unregister"}; !reflect.DeepEqual(manager.calls, want) {
		t.Fatalf("manager calls = %#v, want %#v", manager.calls, want)
	}
}

func TestRunBackgroundCleansUpAfterHealthFailure(t *testing.T) {
	t.Parallel()

	waitErr := errors.New("health timeout")
	manager := &recordingServiceManager{status: NativeStatus{State: NativeStopped}}
	health := &recordingHealthChecker{waitErr: waitErr}
	runner := lifecycleTestRunner(t, manager, health)

	err := runner.runBackground(t.Context())
	if !errors.Is(err, waitErr) {
		t.Fatalf("runBackground() error = %v, want health timeout", err)
	}
	if want := []string{"Status", "Register", "Start", "Stop", "Unregister"}; !reflect.DeepEqual(manager.calls, want) {
		t.Fatalf("manager calls = %#v, want %#v", manager.calls, want)
	}
}

func TestRunBackgroundCleansUpAfterStartFailure(t *testing.T) {
	t.Parallel()

	startErr := errors.New("start failed")
	manager := &recordingServiceManager{status: NativeStatus{State: NativeStopped}, startErr: startErr}
	runner := lifecycleTestRunner(t, manager, &recordingHealthChecker{})

	err := runner.runBackground(t.Context())
	if !errors.Is(err, startErr) {
		t.Fatalf("runBackground() error = %v, want start failed", err)
	}
	if want := []string{"Status", "Register", "Start", "Stop", "Unregister"}; !reflect.DeepEqual(manager.calls, want) {
		t.Fatalf("manager calls = %#v, want %#v", manager.calls, want)
	}
}

func TestStatusBackgroundProjectsRunningStoppedAndFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     NativeStatus
		checkErr   error
		wantCode   int
		wantOutput string
	}{
		{
			name:       "running",
			status:     NativeStatus{Registered: true, State: NativeRunning, PID: 1234},
			wantCode:   exitSuccess,
			wantOutput: "running\npid: 1234\naddress: 127.0.0.1:8080\nlogs: /state/logs\n",
		},
		{name: "stopped", status: NativeStatus{State: NativeStopped}, wantCode: exitStopped, wantOutput: "stopped\n"},
		{name: "failed", status: NativeStatus{Registered: true, State: NativeFailed}, wantCode: exitFailure, wantOutput: "failed\n"},
		{name: "unhealthy", status: NativeStatus{Registered: true, State: NativeRunning, PID: 1234}, checkErr: errors.New("not ready"), wantCode: exitFailure, wantOutput: "failed\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager := &recordingServiceManager{status: test.status, logLocation: "/state/logs"}
			health := &recordingHealthChecker{checkErr: test.checkErr}
			runner := lifecycleTestRunner(t, manager, health)
			stdout, ok := runner.dependencies.stdout.(*bytes.Buffer)
			if !ok {
				t.Fatal("runner stdout is not a bytes.Buffer")
			}

			err := runner.statusBackground(t.Context())
			if got := exitCode(err); got != test.wantCode {
				t.Fatalf("statusBackground() exit = %d, want %d (error %v)", got, test.wantCode, err)
			}
			if got := stdout.String(); got != test.wantOutput {
				t.Fatalf("stdout = %q, want %q", got, test.wantOutput)
			}
		})
	}
}

func TestLifecyclePassesCanceledContextToDependencies(t *testing.T) {
	t.Parallel()

	manager := &recordingServiceManager{status: NativeStatus{State: NativeStopped}}
	health := &recordingHealthChecker{}
	runner := lifecycleTestRunner(t, manager, health)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := runner.runBackground(ctx); err != nil {
		t.Fatalf("runBackground() error = %v", err)
	}
	for _, dependencyContext := range append(manager.contexts, health.contexts...) {
		if !errors.Is(dependencyContext.Err(), context.Canceled) {
			t.Fatalf("dependency context error = %v, want context.Canceled", dependencyContext.Err())
		}
	}
}

func lifecycleTestRunner(t *testing.T, manager *recordingServiceManager, health *recordingHealthChecker) *Runner {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	stateRoot := t.TempDir()
	runner, err := New(Dependencies{
		Stdout: stdout, Stderr: stderr,
		LookupEnv:   func(string) (string, bool) { return "", false },
		UserHomeDir: func() (string, error) { return stateRoot, nil },
		Version:     "0.1.0", InvocationPath: "/opt/homebrew/bin/dataporch",
		ProtectedFileValidator: func(string) error { return nil },
		NewServiceManager:      func(config.Config) (ServiceManager, error) { return manager, nil },
		HealthChecker:          health,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if manager.logLocation == "" {
		manager.logLocation = "/state/logs"
	}
	return runner
}

func TestLifecycleDefinitionContainsOnlyKnownConfiguration(t *testing.T) {
	t.Parallel()

	manager := &recordingServiceManager{status: NativeStatus{State: NativeStopped}}
	runner := lifecycleTestRunner(t, manager, &recordingHealthChecker{})
	if err := runner.runBackground(t.Context()); err != nil {
		t.Fatalf("runBackground() error = %v", err)
	}
	if got := manager.definition.Arguments; !reflect.DeepEqual(got, []string{"run", "-f"}) {
		t.Fatalf("definition arguments = %#v, want [run -f]", got)
	}
	for _, variable := range manager.definition.Environment {
		if variable.Name == "DATAPORCH_MCP_TOKEN" || strings.Contains(variable.Value, "canary") {
			t.Fatalf("definition contains forbidden environment value: %#v", variable)
		}
	}
}

func TestValidateInitializedMapsMissingState(t *testing.T) {
	t.Parallel()

	runner := lifecycleTestRunner(t, &recordingServiceManager{}, &recordingHealthChecker{})
	runner.dependencies.protectedFileValidator = func(string) error { return fs.ErrNotExist }
	err := runner.validateInitialized(config.Config{MasterKeyPath: "/missing/key", SecretsStorePath: "/missing/store"})
	if err == nil || !strings.Contains(err.Error(), "dataporch secrets init") {
		t.Fatalf("validateInitialized() error = %v, want initialization instruction", err)
	}
}

func TestLifecycleDefinitionValidationErrorIsActionable(t *testing.T) {
	t.Parallel()

	runner := lifecycleTestRunner(t, &recordingServiceManager{}, &recordingHealthChecker{})
	runner.dependencies.invocationPath = ""
	_, err := runner.definition(config.Config{MasterKeyPath: "/key", SecretsStorePath: "/store"})
	if err == nil || !strings.Contains(err.Error(), "service executable") {
		t.Fatalf("definition() error = %v, want executable error", err)
	}
}

func TestRecordingManagerUsesContext(t *testing.T) {
	t.Parallel()

	manager := &recordingServiceManager{}
	ctx := context.WithValue(t.Context(), lifecycleContextKey{}, "value")
	manager.record("test", ctx)
	if manager.contexts[0] != ctx {
		t.Fatalf("recorded context = %p, want %p", manager.contexts[0], ctx)
	}
}
