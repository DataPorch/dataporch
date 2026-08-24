package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/adamraziv/dataporch/internal/cli"
	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/secret/local"
	"golang.org/x/term"
)

var releaseVersion string

func ExecuteCLI(ctx context.Context, args []string) int {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	runner, err := cli.New(cli.Dependencies{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, LookupEnv: os.LookupEnv, UserHomeDir: os.UserHomeDir,
		IsTerminal: term.IsTerminal, ReadPassword: term.ReadPassword, ReadConfirmation: readConfirmationLine,
		InitializeSecrets: InitializeSecrets, NewClient: cli.NewUnixClient, NewAdminClient: cli.NewMCPTokenClient,
		Version: releaseVersion, ProtectedFileValidator: local.ValidateProtectedFile, HealthChecker: cli.NewHealthChecker(),
		RunApplication: func(ctx context.Context, cfg config.Config) error {
			application, err := New(cfg, logger)
			if err != nil {
				return fmt.Errorf("creating application: %w", err)
			}
			return application.Run(ctx)
		},
		NewServiceManager: func(config.Config) (cli.ServiceManager, error) {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("resolving user home directory: %w", err)
			}
			return cli.NewNativeServiceManager(runtime.GOOS, home, os.Getuid(), cli.NewCommandRunner(exec.CommandContext))
		},
	})
	if err != nil {
		if _, writeErr := fmt.Fprintf(os.Stderr, "dataporch: %v\n", err); writeErr != nil {
			return 1
		}
		return 1
	}
	return runner.Execute(ctx, args)
}

func readConfirmationLine(stdin *os.File) (string, error) {
	if stdin == nil {
		return "", errors.New("confirmation input is required")
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
