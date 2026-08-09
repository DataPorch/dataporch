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
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := run(logger); err != nil {
		logger.Error("dataporch exited", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	application, err := app.New(cfg, logger)
	if err != nil {
		return fmt.Errorf("creating application: %w", err)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if err := application.Run(ctx); err != nil {
		return fmt.Errorf("running application: %w", err)
	}

	return nil
}
