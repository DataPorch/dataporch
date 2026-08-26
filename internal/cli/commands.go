package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"

	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/connection"
)

var (
	errDatabaseIDRequired    = errors.New("database id is required")
	errDatabaseKindRequired  = errors.New("database kind is required")
	errTerminalRequired      = errors.New("connection import requires an interactive terminal")
	errUnknownCommand        = errors.New("unknown command")
	errUnexpectedArguments   = errors.New("unexpected command arguments")
	ErrMCPRuntimeUnavailable = errors.New("runtime is not running; run dataporch run")
)

const (
	connectionsCommand = "connections"
	importCommand      = "import"
	foregroundFlag     = "-f"
	runCommand         = "run"
)

type RunMCPAdapter func(context.Context, config.Config, io.Reader, io.Writer) error

type ImportClient interface {
	Import(context.Context, connection.ImportRequest) (connection.ImportResult, error)
}
type (
	importClient     = ImportClient
	importClientFunc func(context.Context, connection.ImportRequest) (connection.ImportResult, error)
)

func (f importClientFunc) Import(ctx context.Context, request connection.ImportRequest) (connection.ImportResult, error) {
	return f(ctx, request)
}

type Dependencies struct {
	Stdin                  *os.File
	Stdout, Stderr         io.Writer
	LookupEnv              config.LookupEnv
	UserHomeDir            config.UserHomeDir
	IsTerminal             func(int) bool
	ReadPassword           func(int) ([]byte, error)
	ReadConfirmation       func(*os.File) (string, error)
	InitializeSecrets      func(config.Config) error
	RunApplication         func(context.Context, config.Config) error
	NewClient              func(string) (ImportClient, error)
	NewAdminClient         func(string) (MCPTokenClient, error)
	Version                string
	InvocationPath         string
	ProtectedFileValidator ProtectedFileValidator
	NewServiceManager      func(config.Config) (ServiceManager, error)
	HealthChecker          HealthChecker
	RunMCPAdapter          RunMCPAdapter
}

type commandDependencies struct {
	stdin                   *os.File
	stdout, stderr          io.Writer
	lookupEnv               config.LookupEnv
	userHomeDir             config.UserHomeDir
	isTerminal              func(int) bool
	readPassword            func(int) ([]byte, error)
	readConfirmation        func(*os.File) (string, error)
	initializeSecrets       func(config.Config) error
	runApplication          func(context.Context, config.Config) error
	newClient               func(string) (importClient, error)
	newAdminClient          func(string) (mcpTokenClient, error)
	version, invocationPath string
	protectedFileValidator  ProtectedFileValidator
	newServiceManager       func(config.Config) (ServiceManager, error)
	healthChecker           HealthChecker
	runMCPAdapter           RunMCPAdapter
}

type Runner struct{ dependencies commandDependencies }

func New(dependencies Dependencies) (*Runner, error) {
	path := dependencies.InvocationPath
	if path == "" {
		resolved, err := invocationPath(os.Args[0], exec.LookPath, filepath.Abs)
		if err != nil {
			return nil, fmt.Errorf("resolving invocation path: %w", err)
		}
		path = resolved
	}
	return &Runner{dependencies: commandDependencies{
		stdin: dependencies.Stdin, stdout: dependencies.Stdout, stderr: dependencies.Stderr, lookupEnv: dependencies.LookupEnv, userHomeDir: dependencies.UserHomeDir,
		isTerminal: dependencies.IsTerminal, readPassword: dependencies.ReadPassword, readConfirmation: dependencies.ReadConfirmation,
		initializeSecrets: dependencies.InitializeSecrets, runApplication: dependencies.RunApplication, newClient: dependencies.NewClient, newAdminClient: dependencies.NewAdminClient,
		version: resolvedVersion(dependencies.Version, debug.ReadBuildInfo), invocationPath: path, protectedFileValidator: dependencies.ProtectedFileValidator,
		newServiceManager: dependencies.NewServiceManager, healthChecker: dependencies.HealthChecker, runMCPAdapter: dependencies.RunMCPAdapter,
	}}, nil
}

func (r *Runner) Execute(ctx context.Context, args []string) int {
	err := r.run(ctx, args)
	if err == nil {
		return exitSuccess
	}
	var commandErr *cliError
	if errors.As(err, &commandErr) {
		if !commandErr.silent {
			if writeErr := writeDiagnostic(r.dependencies.stderr, commandErr.message); writeErr != nil {
				return exitFailure
			}
		}
		return commandErr.code
	}
	if writeErr := writeDiagnostic(r.dependencies.stderr, err.Error()); writeErr != nil {
		return exitFailure
	}
	return exitFailure
}

func (r *Runner) run(ctx context.Context, args []string) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if len(args) == 1 && args[0] == "--version" {
		return writeString(r.dependencies.stdout, versionOutput(r.dependencies.version))
	}
	if len(args) == 1 && args[0] == "-l" {
		return writeLongHelp(r.dependencies.stdout, r.dependencies.version, r.dependencies.invocationPath)
	}
	if target, handled, err := helpRequest(args); handled || err != nil {
		if err != nil {
			return err
		}
		if len(target) == 0 {
			return writeRootHelp(r.dependencies.stdout, r.dependencies.version, r.dependencies.invocationPath)
		}
		if target[0] == "dataporch" {
			return writeLongHelp(r.dependencies.stdout, r.dependencies.version, r.dependencies.invocationPath)
		}
		return writeCommandHelp(r.dependencies.stdout, target)
	}
	return runWithContext(ctx, args, r.dependencies)
}

func run(args []string, dependencies commandDependencies) error {
	return runWithContext(context.TODO(), args, dependencies)
}

//nolint:gocyclo // Explicit command grammar remains centralized to preserve exact usage errors.
func runWithContext(ctx context.Context, args []string, dependencies commandDependencies) error {
	switch {
	case len(args) == 2 && args[0] == runCommand && args[1] == foregroundFlag:
		return serve(ctx, dependencies)
	case len(args) == 1 && args[0] == runCommand:
		return (&Runner{dependencies: dependencies}).runBackground(ctx)
	case len(args) == 1 && args[0] == "restart":
		return (&Runner{dependencies: dependencies}).restartBackground(ctx)
	case len(args) == 1 && args[0] == "stop":
		return (&Runner{dependencies: dependencies}).stopBackground(ctx)
	case len(args) == 1 && args[0] == "status":
		return (&Runner{dependencies: dependencies}).statusBackground(ctx)
	case len(args) == 2 && args[0] == "secrets" && args[1] == "init":
		return initializeSecrets(dependencies)
	case len(args) == 1 && args[0] == "mcp":
		return runMCPAdapter(ctx, dependencies)
	case len(args) > 0 && args[0] == "mcp":
		return usageError(errUnexpectedArguments.Error(), errUnexpectedArguments)
	case len(args) >= 2 && args[0] == connectionsCommand && args[1] == importCommand:
		return importConnection(ctx, args[2:], dependencies)
	case len(args) >= 2 && args[0] == mcpTokenCommand:
		return mcpTokenCommandRun(ctx, args[1:], dependencies)
	case len(args) > 0 && args[0] == runCommand && len(args) > 1 && args[1] == "--foreground":
		return usageError("unknown flag --foreground; run dataporch run -h", nil)
	case len(args) > 0:
		return usageError(fmt.Sprintf("unknown command %q; run dataporch --help", args[0]), errUnknownCommand)
	default:
		return usageError("unknown command; run dataporch --help", errUnknownCommand)
	}
}

func runMCPAdapter(ctx context.Context, dependencies commandDependencies) error {
	cfg, err := loadConfig(dependencies)
	if err != nil {
		return err
	}
	if dependencies.protectedFileValidator == nil {
		return errors.New("protected file validator is required")
	}
	for _, path := range []string{cfg.MasterKeyPath, cfg.SecretsStorePath} {
		if err := dependencies.protectedFileValidator(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return errors.New("not initialized; run dataporch secrets init, then dataporch run")
			}
			return fmt.Errorf("validating local state %q: %w", path, err)
		}
	}
	if dependencies.runMCPAdapter == nil {
		return errors.New("MCP stdio adapter is required")
	}
	if err := dependencies.runMCPAdapter(ctx, cfg, dependencies.stdin, dependencies.stdout); err != nil {
		if errors.Is(err, ErrMCPRuntimeUnavailable) {
			return ErrMCPRuntimeUnavailable
		}
		return err
	}

	return nil
}

func serve(ctx context.Context, dependencies commandDependencies) error {
	cfg, err := loadConfig(dependencies)
	if err != nil {
		return err
	}
	if dependencies.runApplication == nil {
		return errors.New("application runner is required")
	}
	if err := dependencies.runApplication(ctx, cfg); err != nil {
		return fmt.Errorf("running application: %w", err)
	}
	return nil
}

func initializeSecrets(dependencies commandDependencies) error {
	cfg, err := loadConfig(dependencies)
	if err != nil {
		return err
	}
	if dependencies.initializeSecrets == nil {
		return errors.New("secret initializer is required")
	}
	if err := dependencies.initializeSecrets(cfg); err != nil {
		return fmt.Errorf("initializing secrets: %w", err)
	}
	return nil
}

func importConnection(ctx context.Context, args []string, dependencies commandDependencies) error {
	arguments, err := parseImportArguments(args)
	if err != nil {
		return usageError(err.Error(), err)
	}
	connectionString, err := readConnectionString(dependencies)
	if err != nil {
		return err
	}
	defer zeroBytes(connectionString)
	cfg, err := loadConfig(dependencies)
	if err != nil {
		return err
	}
	if dependencies.newClient == nil {
		return errors.New("connection import client is required")
	}
	client, err := dependencies.newClient(cfg.AdminSocketPath)
	if err != nil {
		return fmt.Errorf("creating connection import client: %w", err)
	}
	result, err := client.Import(ctx, connection.ImportRequest{ID: arguments.databaseID, Kind: arguments.kind, ConnectionString: connectionString})
	if err != nil {
		return fmt.Errorf("importing connection: %w", err)
	}
	verb := "added"
	if result.IsUpdated {
		verb = "updated"
	}
	return writeString(dependencies.stdout, fmt.Sprintf("Database %q was %s successfully and its connection has not been tested.\n", arguments.databaseID, verb))
}

type importArguments struct {
	databaseID connection.ID
	kind       connection.Kind
}

func parseImportArguments(args []string) (importArguments, error) {
	flags := flag.NewFlagSet("connections import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databaseID := flags.String("id", "", "database identifier")
	kind := flags.String("kind", "", "database adapter kind")
	if err := flags.Parse(args); err != nil {
		return importArguments{}, fmt.Errorf("parsing import command: %w", err)
	}
	if len(flags.Args()) != 0 {
		return importArguments{}, errUnexpectedArguments
	}
	if *databaseID == "" {
		return importArguments{}, errDatabaseIDRequired
	}
	if *kind == "" {
		return importArguments{}, errDatabaseKindRequired
	}
	return importArguments{databaseID: connection.ID(*databaseID), kind: connection.Kind(*kind)}, nil
}

func readConnectionString(dependencies commandDependencies) ([]byte, error) {
	if dependencies.stdin == nil || dependencies.isTerminal == nil || dependencies.readPassword == nil || !dependencies.isTerminal(int(dependencies.stdin.Fd())) {
		return nil, errTerminalRequired
	}
	if err := writeString(dependencies.stdout, "Connection string: "); err != nil {
		return nil, err
	}
	value, err := dependencies.readPassword(int(dependencies.stdin.Fd()))
	if newlineErr := writeString(dependencies.stdout, "\n"); newlineErr != nil {
		return value, newlineErr
	}
	if err != nil {
		return value, fmt.Errorf("reading connection string: %w", err)
	}
	return value, nil
}

func loadConfig(dependencies commandDependencies) (config.Config, error) {
	if dependencies.lookupEnv == nil {
		return config.Config{}, errors.New("configuration lookup is required")
	}
	var cfg config.Config
	var err error
	if dependencies.userHomeDir == nil {
		cfg, err = config.Load(dependencies.lookupEnv)
	} else {
		cfg, err = config.Load(dependencies.lookupEnv, dependencies.userHomeDir)
	}
	if err != nil {
		return config.Config{}, fmt.Errorf("loading configuration: %w", err)
	}
	return cfg, nil
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
