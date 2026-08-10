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

func run(args []string, dependencies commandDependencies) error {
	switch {
	case len(args) == 0:
		return serve(dependencies)
	case len(args) == 2 && args[0] == "secrets" && args[1] == "init":
		return initializeSecrets(dependencies)
	case len(args) >= 2 && args[0] == "connections" && args[1] == "import":
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
	flags := flag.NewFlagSet("connections import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databaseID := flags.String("id", "", "database identifier")
	kind := flags.String("kind", "", "database adapter kind")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing import command: %w", err)
	}
	if len(flags.Args()) != 0 {
		return errUnexpectedArguments
	}
	if *databaseID == "" {
		return errDatabaseIDRequired
	}
	if *kind == "" {
		return errDatabaseKindRequired
	}
	hasStdin := dependencies.stdin != nil
	hasTerminalCheck := dependencies.isTerminal != nil
	hasPasswordReader := dependencies.readPassword != nil
	if !hasStdin || !hasTerminalCheck || !hasPasswordReader {
		return errTerminalRequired
	}
	fd := int(dependencies.stdin.Fd())
	if !dependencies.isTerminal(fd) {
		return errTerminalRequired
	}
	if dependencies.stdout == nil {
		return errors.New("standard output is required")
	}
	fmt.Fprint(dependencies.stdout, "Connection string: ")
	connectionString, err := dependencies.readPassword(fd)
	fmt.Fprintln(dependencies.stdout)
	if err != nil {
		return fmt.Errorf("reading connection string: %w", err)
	}
	defer clear(connectionString)

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
		ID:               connection.ID(*databaseID),
		Kind:             connection.Kind(*kind),
		ConnectionString: connectionString,
	})
	if err != nil {
		return fmt.Errorf("importing connection: %w", err)
	}
	verb := "added"
	if result.IsUpdated {
		verb = "updated"
	}
	fmt.Fprintf(
		dependencies.stdout,
		"Database %q was %s successfully and its connection has not been tested.\n",
		*databaseID,
		verb,
	)
	return nil
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

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
