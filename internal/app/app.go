package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/adamraziv/dataporch/internal/access"
	"github.com/adamraziv/dataporch/internal/catalog"
	"github.com/adamraziv/dataporch/internal/catalog/memory"
	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/execution"
	"github.com/adamraziv/dataporch/internal/transports/httpapi"
	mcptransport "github.com/adamraziv/dataporch/internal/transports/mcp"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 1 << 20
)

var (
	errContextRequired = errors.New("app: context is required")
	errLoggerRequired  = errors.New("app: logger is required")
)

type App struct {
	server         *http.Server
	logger         *slog.Logger
	shutdownPeriod time.Duration
}

func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		return nil, errLoggerRequired
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating configuration: %w", err)
	}

	connector, err := memory.New([]catalog.Resource{
		{
			URI:         "memory://customers",
			Name:        "Customers",
			Kind:        "table",
			Description: "Bootstrap customer resource",
		},
		{
			URI:         "memory://orders",
			Name:        "Orders",
			Kind:        "table",
			Description: "Bootstrap order resource",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating memory connector: %w", err)
	}

	service, err := execution.New(
		connector,
		access.NewAllowAll(),
		cfg.ResourceLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("creating execution service: %w", err)
	}

	httpHandler, err := httpapi.New(service, cfg.ResourceLimit, logger)
	if err != nil {
		return nil, fmt.Errorf("creating http adapter: %w", err)
	}

	mcpHandler, err := mcptransport.New(service, cfg.ResourceLimit, logger)
	if err != nil {
		return nil, fmt.Errorf("creating mcp adapter: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/", httpHandler)

	return &App{
		server: &http.Server{
			Addr:              cfg.HTTPAddress,
			Handler:           mux,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
		},
		logger:         logger,
		shutdownPeriod: cfg.ShutdownPeriod,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	if ctx == nil {
		return errContextRequired
	}

	if ctx.Err() != nil {
		return nil
	}

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", a.server.Addr)
	if err != nil {
		return fmt.Errorf("listening on %q: %w", a.server.Addr, err)
	}

	a.logger.InfoContext(
		ctx,
		"dataporch listening",
		slog.String("address", listener.Addr().String()),
	)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- a.server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("serving http: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		a.shutdownPeriod,
	)
	defer cancel()

	shutdownErr := a.server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, a.server.Close())
	}

	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("serving http: %w", err))
	}

	if shutdownErr != nil {
		return fmt.Errorf("shutting down http server: %w", shutdownErr)
	}

	a.logger.InfoContext(shutdownCtx, "dataporch stopped")

	return nil
}
