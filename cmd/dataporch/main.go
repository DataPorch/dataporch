package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/adamraziv/dataporch/internal/app"
)

func main() { os.Exit(runMain()) }

func runMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return executeWithCleanup(ctx, stop, os.Args[1:], app.ExecuteCLI)
}

func executeWithCleanup(ctx context.Context, cancel context.CancelFunc, args []string, execute func(context.Context, []string) int) int {
	defer cancel()
	return execute(ctx, args)
}
