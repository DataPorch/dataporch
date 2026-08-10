package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/connection"
)

var (
	errDatabaseIDRequired   = errors.New("database id is required")
	errDatabaseKindRequired = errors.New("database kind is required")
	errTerminalRequired     = errors.New("connection import requires an interactive terminal")
	errUnknownCommand       = errors.New("unknown command")
	errUnexpectedArguments  = errors.New("unexpected command arguments")
)

const (
	connectionsCommand = "connections"
	importCommand      = "import"
)

type importClient interface {
	Import(context.Context, connection.ImportRequest) (connection.ImportResult, error)
}

type importClientFunc func(context.Context, connection.ImportRequest) (connection.ImportResult, error)

func (f importClientFunc) Import(
	ctx context.Context,
	request connection.ImportRequest,
) (connection.ImportResult, error) {
	return f(ctx, request)
}

type commandDependencies struct {
	stdin             *os.File
	stdout            io.Writer
	stderr            io.Writer
	lookupEnv         config.LookupEnv
	isTerminal        func(int) bool
	readPassword      func(int) ([]byte, error)
	initializeSecrets func(config.Config) error
	runApplication    func(context.Context, config.Config) error
	newClient         func(string) (importClient, error)
}

type importArguments struct {
	databaseID connection.ID
	kind       connection.Kind
}

func run(args []string, dependencies commandDependencies) error {
	switch {
	case len(args) == 0:
		return serve(dependencies)
	case len(args) == 2 && args[0] == "secrets" && args[1] == "init":
		return initializeSecrets(dependencies)
	case len(args) >= 2 && args[0] == connectionsCommand && args[1] == importCommand:
		return importConnection(args[2:], dependencies)
	default:
		return errUnknownCommand
	}
}

func serve(dependencies commandDependencies) error {
	cfg, err := loadConfig(dependencies)
	if err != nil {
		return err
	}

	if dependencies.runApplication == nil {
		return errors.New("application runner is required")
	}

	if err := dependencies.runApplication(context.Background(), cfg); err != nil {
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

func importConnection(args []string, dependencies commandDependencies) error {
	arguments, err := parseImportArguments(args)
	if err != nil {
		return err
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

	result, err := client.Import(context.Background(), connection.ImportRequest{
		ID:               arguments.databaseID,
		Kind:             arguments.kind,
		ConnectionString: connectionString,
	})
	if err != nil {
		return fmt.Errorf("importing connection: %w", err)
	}

	verb := "added"
	if result.IsUpdated {
		verb = "updated"
	}

	_, _ = fmt.Fprintf(
		dependencies.stdout,
		"Database %q was %s successfully and its connection has not been tested.\n",
		arguments.databaseID,
		verb,
	)

	return nil
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

	return importArguments{
		databaseID: connection.ID(*databaseID),
		kind:       connection.Kind(*kind),
	}, nil
}

func readConnectionString(dependencies commandDependencies) ([]byte, error) {
	hasStdin := dependencies.stdin != nil
	hasTerminalCheck := dependencies.isTerminal != nil

	hasPasswordReader := dependencies.readPassword != nil
	if !hasStdin || !hasTerminalCheck || !hasPasswordReader {
		return nil, errTerminalRequired
	}

	fd := int(dependencies.stdin.Fd())
	if !dependencies.isTerminal(fd) {
		return nil, errTerminalRequired
	}

	if dependencies.stdout == nil {
		return nil, errors.New("standard output is required")
	}

	_, _ = fmt.Fprint(dependencies.stdout, "Connection string: ")
	connectionString, err := dependencies.readPassword(fd)
	_, _ = fmt.Fprintln(dependencies.stdout)

	if err != nil {
		return connectionString, fmt.Errorf("reading connection string: %w", err)
	}

	return connectionString, nil
}

func loadConfig(dependencies commandDependencies) (config.Config, error) {
	if dependencies.lookupEnv == nil {
		return config.Config{}, errors.New("configuration lookup is required")
	}

	cfg, err := config.Load(dependencies.lookupEnv)
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
