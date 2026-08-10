package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/connection/filestore"
	"github.com/adamraziv/dataporch/internal/secret"
	"github.com/adamraziv/dataporch/internal/secret/local"
	"github.com/adamraziv/dataporch/internal/transports/localadmin"
)

var errSecurityUnavailable = errors.New("security component unavailable")

type securityComponents struct {
	manager     *connection.Manager
	adminServer *localadmin.Server
}

func newSecurityComponents(
	cfg config.Config,
	logger *slog.Logger,
	adapters ...connection.Adapter,
) (securityComponents, error) {
	connector, err := connection.NewConnector(adapters...)
	if err != nil {
		return securityComponents{}, err
	}

	resolver, writer := openSecretStore(cfg, logger)
	repository, definitions := openDefinitionStore(cfg, logger)

	manager, err := connection.NewManager(resolver, definitions)
	if err != nil {
		return securityComponents{}, err
	}

	importer, err := connection.NewImporter(connection.ImporterDependencies{
		Adapters:    connector,
		Secrets:     writer,
		Definitions: repository,
		Registrar:   manager,
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
		return securityComponents{}, err
	}

	handler, err := localadmin.NewHandler(importer, logger)
	if err != nil {
		return securityComponents{}, err
	}

	server, err := localadmin.NewServer(cfg.AdminSocketPath, handler, logger)
	if err != nil {
		return securityComponents{}, err
	}

	return securityComponents{manager: manager, adminServer: server}, nil
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
