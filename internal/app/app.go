package app

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/adamraziv/dataporch/internal/access"
	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	mcpControlLocal "github.com/adamraziv/dataporch/internal/mcpcontrol/local"
	"github.com/adamraziv/dataporch/internal/transports/httpapi"
	"github.com/adamraziv/dataporch/internal/transports/localadmin"
	"github.com/adamraziv/dataporch/internal/transports/localmcp"
	"github.com/adamraziv/dataporch/internal/transports/mcp"
	"github.com/adamraziv/dataporch/internal/transports/mcpauth"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 35 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 1 << 20
)

var (
	errContextRequired                 = errors.New("app: context is required")
	errLoggerRequired                  = errors.New("app: logger is required")
	errExecutionServiceFactoryRequired = errors.New("app: execution service factory is required")
	errRandomnessRequired              = errors.New("app: randomness source is required")
)

type executionServiceFactory func(execution.Dependencies) (*execution.Service, error)

type appDependencies struct {
	relationalModuleFactories []relationalModuleFactory
	newExecutionService       executionServiceFactory
	random                    io.Reader
}

type transportLifecycle interface {
	Run(context.Context) error
}

type App struct {
	server         *http.Server
	adminServer    *localadmin.Server
	localMCPServer transportLifecycle
	manager        *connection.Manager
	service        *execution.Service
	runtimes       []runtimeLifecycle
	logger         *slog.Logger
	shutdownPeriod time.Duration
}

func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	return newWithDependencies(cfg, logger, appDependencies{
		relationalModuleFactories: []relationalModuleFactory{
			newPostgresModule,
			newSQLiteModule,
			newMySQLModule,
		},
		newExecutionService: execution.New,
		random:              cryptorand.Reader,
	})
}

func newWithDependencies(
	cfg config.Config,
	logger *slog.Logger,
	dependencies appDependencies,
) (*App, error) {
	if logger == nil {
		return nil, errLoggerRequired
	}

	if dependencies.newExecutionService == nil {
		return nil, errExecutionServiceFactoryRequired
	}

	if dependencies.random == nil {
		return nil, errRandomnessRequired
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating configuration: %w", err)
	}

	security, err := newSecurityComponents(cfg, logger, dependencies)
	if err != nil {
		return nil, fmt.Errorf("creating security components: %w", err)
	}

	service, err := dependencies.newExecutionService(execution.Dependencies{
		Sources:                  security.manager,
		Authorizer:               access.New(),
		MaxLimit:                 cfg.ResourceLimit,
		RelationalDiscoverers:    security.relational.discoverers,
		RelationalQueryExecutors: security.relational.queryExecutors,
	})
	if err != nil {
		return nil, joinRuntimeCleanup(
			fmt.Errorf("creating execution service: %w", err),
			cfg.ShutdownPeriod,
			security.relational.runtimes,
		)
	}

	httpHandler, err := httpapi.New(logger)
	if err != nil {
		return nil, joinRuntimeCleanup(
			fmt.Errorf("creating http adapter: %w", err),
			cfg.ShutdownPeriod,
			security.relational.runtimes,
		)
	}

	mcpHandler, err := mcp.New(mcp.Dependencies{
		Discoverer:             service,
		RelationalQuerier:      service,
		QueryResponseByteLimit: cfg.QueryResponseByteLimit,
		Logger:                 logger,
	})
	if err != nil {
		return nil, joinRuntimeCleanup(
			fmt.Errorf("creating mcp adapter: %w", err),
			cfg.ShutdownPeriod,
			security.relational.runtimes,
		)
	}

	localCredentials, err := mcpControlLocal.New(cfg.MCPControlTokenPath)
	if err != nil {
		return nil, joinRuntimeCleanup(
			fmt.Errorf("creating local MCP credential store: %w", err),
			cfg.ShutdownPeriod,
			security.relational.runtimes,
		)
	}
	localMCPServer, err := localmcp.NewServer(localmcp.Dependencies{
		SocketPath:  cfg.MCPSocketPath,
		Credentials: localCredentials,
		Random:      dependencies.random,
		Handler:     mcpHandler,
	})
	if err != nil {
		return nil, joinRuntimeCleanup(
			fmt.Errorf("creating local MCP server: %w", err),
			cfg.ShutdownPeriod,
			security.relational.runtimes,
		)
	}

	authenticatedMCP, err := mcpauth.New(security.mcpTokens, mcpHandler)
	if err != nil {
		return nil, joinRuntimeCleanup(
			fmt.Errorf("creating mcp auth adapter: %w", err),
			cfg.ShutdownPeriod,
			security.relational.runtimes,
		)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", authenticatedMCP)
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
		adminServer:    security.adminServer,
		localMCPServer: localMCPServer,
		manager:        security.manager,
		service:        service,
		runtimes:       slices.Clone(security.relational.runtimes),
		logger:         logger,
		shutdownPeriod: cfg.ShutdownPeriod,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	if ctx == nil {
		return errContextRequired
	}

	if ctx.Err() != nil {
		return a.closeRuntimesWithTimeout(ctx)
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
	localMCPErrors := a.serveLocalMCP(runCtx)

	return a.waitForServers(
		ctx,
		cancel,
		publicErrors,
		adminErrors,
		localMCPErrors,
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

func (a *App) serveLocalMCP(ctx context.Context) <-chan error {
	if a.localMCPServer == nil {
		return nil
	}

	errors := make(chan error, 1)
	go func() { errors <- a.localMCPServer.Run(ctx) }()

	return errors
}

func (a *App) waitForServers(
	ctx context.Context,
	cancel context.CancelFunc,
	publicErrors <-chan error,
	adminErrors <-chan error,
	localMCPErrors <-chan error,
) error {
	for {
		if localMCPErrors != nil {
			select {
			case err := <-localMCPErrors:
				if err == nil {
					localMCPErrors = nil
					continue
				}
				return a.failLocalMCP(ctx, cancel, publicErrors, adminErrors, err)
			default:
			}
		}

		select {
		case err := <-publicErrors:
			cancel()
			a.waitForAdmin(adminErrors)
			a.waitForLocalMCP(localMCPErrors)
			runtimeErr := a.closeRuntimesWithTimeout(ctx)

			if errors.Is(err, http.ErrServerClosed) {
				return runtimeErr
			}

			return errors.Join(fmt.Errorf("serving http: %w", err), runtimeErr)
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
		case err := <-localMCPErrors:
			if err == nil {
				localMCPErrors = nil
				continue
			}
			return a.failLocalMCP(ctx, cancel, publicErrors, adminErrors, err)
		case <-ctx.Done():
			return a.shutdown(
				ctx,
				cancel,
				publicErrors,
				adminErrors,
				localMCPErrors,
			)
		}
	}
}

func (a *App) failLocalMCP(
	ctx context.Context,
	cancel context.CancelFunc,
	publicErrors <-chan error,
	adminErrors <-chan error,
	err error,
) error {
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), a.shutdownPeriod)
	shutdownErr := a.server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, a.server.Close())
	}
	stop()
	if publicErr := <-publicErrors; publicErr != nil && !errors.Is(publicErr, http.ErrServerClosed) {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("serving http: %w", publicErr))
	}
	a.waitForAdmin(adminErrors)
	runtimeErr := a.closeRuntimesWithTimeout(ctx)

	return errors.Join(fmt.Errorf("serving local MCP: %w", err), shutdownErr, runtimeErr)
}

func (a *App) shutdown(
	ctx context.Context,
	cancel context.CancelFunc,
	publicErrors <-chan error,
	adminErrors <-chan error,
	localMCPErrors <-chan error,
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
	if err := a.waitForLocalMCP(localMCPErrors); err != nil {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("serving local MCP: %w", err))
	}

	shutdownErr = errors.Join(shutdownErr, a.closeRuntimes(shutdownCtx))
	if shutdownErr != nil {
		return fmt.Errorf("shutting down application: %w", shutdownErr)
	}

	a.logger.InfoContext(shutdownCtx, "dataporch stopped")

	return nil
}

func (a *App) closeRuntimesWithTimeout(ctx context.Context) error {
	shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), a.shutdownPeriod)
	defer stop()

	return a.closeRuntimes(shutdownCtx)
}

func (a *App) closeRuntimes(ctx context.Context) error {
	return closeRuntimeLifecycles(ctx, a.runtimes)
}

func (a *App) waitForAdmin(adminErrors <-chan error) {
	if adminErrors == nil {
		return
	}

	if err := <-adminErrors; err != nil {
		a.logger.Warn("admin transport unavailable", "category", "unavailable")
	}
}

func (a *App) waitForLocalMCP(localMCPErrors <-chan error) error {
	if localMCPErrors == nil {
		return nil
	}

	return <-localMCPErrors
}
