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
	mcpControlLocal "github.com/adamraziv/dataporch/internal/mcpcontrol/local"
	"github.com/adamraziv/dataporch/internal/secret/local"
	"github.com/adamraziv/dataporch/internal/transports/mcpstdio"
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
		RunMCPAdapter: func(ctx context.Context, cfg config.Config, input io.Reader, output io.Writer) error {
			credentials, err := mcpControlLocal.New(cfg.MCPControlTokenPath)
			if err != nil {
				return fmt.Errorf("creating local MCP credential reader: %w", err)
			}
			proxy, err := mcpstdio.New(mcpstdio.Dependencies{
				Input: input, Output: output, SocketPath: cfg.MCPSocketPath, Credentials: credentials,
			})
			if err != nil {
				return fmt.Errorf("creating MCP stdio adapter: %w", err)
			}
			if err := proxy.Run(ctx); err != nil {
				if errors.Is(err, mcpstdio.ErrRuntimeUnavailable) {
					return cli.ErrMCPRuntimeUnavailable
				}
				return fmt.Errorf("running MCP stdio adapter: %w", err)
			}
			return nil
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
