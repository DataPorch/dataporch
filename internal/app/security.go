package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/DataPorch/dataporch/internal/config"
	"github.com/DataPorch/dataporch/internal/connection"
	"github.com/DataPorch/dataporch/internal/connection/filestore"
	"github.com/DataPorch/dataporch/internal/mcptoken"
	mcpTokenLocal "github.com/DataPorch/dataporch/internal/mcptoken/local"
	"github.com/DataPorch/dataporch/internal/secret"
	"github.com/DataPorch/dataporch/internal/secret/local"
	"github.com/DataPorch/dataporch/internal/transports/localadmin"
)

var errSecurityUnavailable = errors.New("security component unavailable")

type securityComponents struct {
	manager     *connection.Manager
	mcpTokens   *mcptoken.Service
	adminServer *localadmin.Server
	relational  relationalComposition
}

func newSecurityComponents(
	cfg config.Config,
	logger *slog.Logger,
	dependencies appDependencies,
) (securityComponents, error) {
	resolver, writer := openSecretStore(cfg, logger)
	repository, definitions := openDefinitionStore(cfg, logger)

	manager, err := connection.NewManager(resolver, definitions)
	if err != nil {
		return securityComponents{}, err
	}

	relational, err := newRelationalComposition(manager, relationalCompositionOptions{
		factories: dependencies.relationalModuleFactories,
		policy: queryPolicy{
			timeout:           cfg.QueryTimeout,
			responseByteLimit: cfg.QueryResponseByteLimit,
			truncationEnabled: cfg.QueryTruncationEnabled,
			rowLimit:          cfg.QueryRowLimit,
		},
		cleanupPeriod: cfg.ShutdownPeriod,
	})
	if err != nil {
		return securityComponents{}, err
	}

	cleanup := func(cause error) (securityComponents, error) {
		return securityComponents{}, joinRuntimeCleanup(
			cause,
			cfg.ShutdownPeriod,
			relational.runtimes,
		)
	}

	connector, err := connection.NewConnector(relational.adapters...)
	if err != nil {
		return cleanup(err)
	}

	registrar, err := newReplacementRegistrar(
		manager,
		relational.runtimeByKind,
	)
	if err != nil {
		return cleanup(err)
	}

	importer, err := connection.NewImporter(connection.ImporterDependencies{
		Adapters:    connector,
		Secrets:     writer,
		Definitions: repository,
		Registrar:   registrar,
		Warn: func(databaseID connection.ID, category string) {
			logger.Warn(
				"connection import cleanup incomplete",
				"database_id",
				databaseID,
				"category",
				category,
			)
		},
	})
	if err != nil {
		return cleanup(err)
	}

	tokenStore, err := mcpTokenLocal.New(cfg.MCPTokenStorePath)
	if err != nil {
		return cleanup(fmt.Errorf("creating mcp token store: %w", err))
	}

	mcpTokens, err := mcptoken.New(tokenStore, time.Now)
	if err != nil {
		return cleanup(fmt.Errorf("creating mcp token service: %w", err))
	}

	handler, err := localadmin.NewHandler(importer, mcpTokens, logger)
	if err != nil {
		return cleanup(err)
	}

	server, err := localadmin.NewServer(cfg.AdminSocketPath, handler, logger)
	if err != nil {
		return cleanup(err)
	}

	return securityComponents{
		manager:     manager,
		mcpTokens:   mcpTokens,
		adminServer: server,
		relational:  relational,
	}, nil
}

func openSecretStore(cfg config.Config, logger *slog.Logger) (connection.SecretResolver, connection.SecretWriter) {
	store, err := local.Open(local.Paths{KeyPath: cfg.MasterKeyPath, StorePath: cfg.SecretsStorePath})
	if err == nil {
		return store, store
	}

	logUnavailable(logger, "local_secret_store", err)

	unavailable := unavailableSecrets{}

	return unavailable, unavailable
}

func openDefinitionStore(
	cfg config.Config,
	logger *slog.Logger,
) (connection.DefinitionRepository, []connection.Definition) {
	store, err := filestore.Open(cfg.ConnectionsStorePath)
	if err != nil {
		logUnavailable(logger, "connection_store", err)
		return unavailableDefinitions{}, nil
	}

	definitions, err := store.List(context.Background())
	if err != nil {
		logUnavailable(logger, "connection_store", err)
		return unavailableDefinitions{}, nil
	}

	return store, definitions
}

func logUnavailable(logger *slog.Logger, component string, err error) {
	logger.Warn(
		"security component unavailable",
		"component",
		component,
		"category",
		securityErrorCategory(err),
	)
}

func securityErrorCategory(err error) string {
	switch {
	case errors.Is(err, local.ErrStoreCorrupt), errors.Is(err, filestore.ErrStoreCorrupt):
		return "corrupt"
	case errors.Is(err, local.ErrInvalidPermissions), errors.Is(err, filestore.ErrInvalidPermissions):
		return "invalid_permissions"
	default:
		return "unavailable"
	}
}

type unavailableSecrets struct{}

func (unavailableSecrets) Store(context.Context, []byte) (secret.Reference, error) {
	return "", errSecurityUnavailable
}

func (unavailableSecrets) Resolve(context.Context, secret.Reference) ([]byte, error) {
	return nil, errSecurityUnavailable
}

func (unavailableSecrets) Delete(context.Context, secret.Reference) error {
	return errSecurityUnavailable
}

type unavailableDefinitions struct{}

func (unavailableDefinitions) Lookup(context.Context, connection.ID) (connection.Definition, error) {
	return connection.Definition{}, errSecurityUnavailable
}

func (unavailableDefinitions) Upsert(context.Context, connection.Definition) error {
	return errSecurityUnavailable
}
