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
	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	"github.com/adamraziv/dataporch/internal/transports/httpapi"
	"github.com/adamraziv/dataporch/internal/transports/localadmin"
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
	adminServer    *localadmin.Server
	manager        *connection.Manager
	logger         *slog.Logger
	shutdownPeriod time.Duration
}

func New(cfg config.Config, logger *slog.Logger, adapters ...connection.Adapter) (*App, error) {
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
	security, err := newSecurityComponents(cfg, logger, adapters...)
	if err != nil {
		return nil, fmt.Errorf("creating security components: %w", err)
	}

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
		adminServer:    security.adminServer,
		manager:        security.manager,
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

	listener, err := a.listenPublic(ctx)
	if err != nil {
		return err
	}

	a.logger.InfoContext(
		ctx,
		"dataporch listening",
		slog.String("address", listener.Addr().String()),
	)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	publicErrors := a.servePublic(listener)
	adminErrors := a.serveAdmin(runCtx)
	return a.waitForServers(
		ctx,
		cancel,
		publicErrors,
		adminErrors,
	)
}

func (a *App) listenPublic(ctx context.Context) (net.Listener, error) {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", a.server.Addr)
	if err != nil {
		return nil, fmt.Errorf("listening on %q: %w", a.server.Addr, err)
	}
	return listener, nil
}

func (a *App) servePublic(listener net.Listener) <-chan error {
	errors := make(chan error, 1)
	go func() { errors <- a.server.Serve(listener) }()
	return errors
}

func (a *App) serveAdmin(ctx context.Context) <-chan error {
	errors := make(chan error, 1)
	go func() { errors <- a.adminServer.Run(ctx) }()
	return errors
}

func (a *App) waitForServers(
	ctx context.Context,
	cancel context.CancelFunc,
	publicErrors <-chan error,
	adminErrors <-chan error,
) error {
	for {
		select {
		case err := <-publicErrors:
			cancel()
			a.waitForAdmin(adminErrors)
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("serving http: %w", err)
		case err := <-adminErrors:
			if err != nil {
				a.logger.WarnContext(
					ctx,
					"admin transport unavailable",
					"category",
					"unavailable",
				)
			}
			adminErrors = nil
		case <-ctx.Done():
			return a.shutdown(
				ctx,
				cancel,
				publicErrors,
				adminErrors,
			)
		}
	}
}

func (a *App) shutdown(
	ctx context.Context,
	cancel context.CancelFunc,
	publicErrors <-chan error,
	adminErrors <-chan error,
) error {
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), a.shutdownPeriod)
	defer stop()
	shutdownErr := a.server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, a.server.Close())
	}
	if err := <-publicErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("serving http: %w", err))
	}
	a.waitForAdmin(adminErrors)
	if shutdownErr != nil {
		return fmt.Errorf("shutting down http server: %w", shutdownErr)
	}
	a.logger.InfoContext(shutdownCtx, "dataporch stopped")
	return nil
}

func (a *App) waitForAdmin(adminErrors <-chan error) {
	if adminErrors == nil {
		return
	}
	if err := <-adminErrors; err != nil {
		a.logger.Warn("admin transport unavailable", "category", "unavailable")
	}
}
