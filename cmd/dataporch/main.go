package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/adamraziv/dataporch/internal/app"
	"github.com/adamraziv/dataporch/internal/config"
	"golang.org/x/term"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := run(os.Args[1:], productionDependencies(logger)); err != nil {
		logger.Error("dataporch exited", slog.Any("error", err))
		os.Exit(1)
	}
}

func productionDependencies(logger *slog.Logger) commandDependencies {
	return commandDependencies{
		stdin:             os.Stdin,
		stdout:            os.Stdout,
		stderr:            os.Stderr,
		lookupEnv:         os.LookupEnv,
		isTerminal:        term.IsTerminal,
		readPassword:      term.ReadPassword,
		initializeSecrets: app.InitializeSecrets,
		newClient: func(socketPath string) (importClient, error) {
			return newUnixClient(socketPath)
		},
		runApplication: func(ctx context.Context, cfg config.Config) error {
			application, err := app.New(cfg, logger)
			if err != nil {
				return fmt.Errorf("creating application: %w", err)
			}
			return runApplication(ctx, application)
		},
	}
}

func runApplication(ctx context.Context, application *app.App) error {
	ctx, stop := signal.NotifyContext(
		ctx,
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if err := application.Run(ctx); err != nil {
		return err
	}

	return nil
}
