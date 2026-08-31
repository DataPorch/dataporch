package connection

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/DataPorch/dataporch/internal/secret"
)

const oldSecretCleanupFailed = "old_secret_cleanup_failed"

var (
	ErrInvalidConnectionString = errors.New("connection: invalid connection string")
	ErrImportUnavailable       = errors.New("connection: import unavailable")
)

type ImportRequest struct {
	ID               ID
	Kind             Kind
	ConnectionString []byte
}

type ImportResult struct {
	ID                 ID
	IsUpdated          bool
	IsConnectionTested bool
}

type SecretWriter interface {
	Store(context.Context, []byte) (secret.Reference, error)
	Delete(context.Context, secret.Reference) error
}

type DefinitionRepository interface {
	Lookup(context.Context, ID) (Definition, error)
	Upsert(context.Context, Definition) error
}

type DefinitionRegistrar interface {
	Register(Definition) (RegistrationResult, error)
}

type AdapterResolver interface {
	Resolve(Kind) (Adapter, error)
}

type CleanupWarning func(databaseID ID, category string)

type ImporterDependencies struct {
	Adapters    AdapterResolver
	Secrets     SecretWriter
	Definitions DefinitionRepository
	Registrar   DefinitionRegistrar
	Warn        CleanupWarning
}

type Importer struct {
	adapters    AdapterResolver
	secrets     SecretWriter
	definitions DefinitionRepository
	registrar   DefinitionRegistrar
	warn        CleanupWarning
}

func NewImporter(dependencies ImporterDependencies) (*Importer, error) {
	hasAdapters := dependencies.Adapters != nil
	hasSecrets := dependencies.Secrets != nil
	hasDefinitions := dependencies.Definitions != nil

	hasRegistrar := dependencies.Registrar != nil
	if !hasAdapters || !hasSecrets || !hasDefinitions || !hasRegistrar {
		return nil, ErrImportUnavailable
	}

	if dependencies.Warn == nil {
		dependencies.Warn = func(ID, string) {}
	}

	return &Importer{
		adapters:    dependencies.Adapters,
		secrets:     dependencies.Secrets,
		definitions: dependencies.Definitions,
		registrar:   dependencies.Registrar,
		warn:        dependencies.Warn,
	}, nil
}

func (i *Importer) Import(ctx context.Context, request ImportRequest) (ImportResult, error) {
	if ctx == nil || ctx.Err() != nil {
		return ImportResult{}, ErrImportUnavailable
	}

	if err := (Definition{ID: request.ID, Kind: request.Kind}).Validate(); err != nil {
		return ImportResult{}, fmt.Errorf("%w: invalid database identity", ErrImportUnavailable)
	}

	adapter, err := i.adapters.Resolve(request.Kind)
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w: adapter unavailable", ErrImportUnavailable)
	}

	input := append([]byte(nil), request.ConnectionString...)
	defer zeroBytes(input)

	parsed, err := adapter.ParseConnectionString(input)
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w: %s", ErrInvalidConnectionString, request.Kind)
	}
	defer clearParsed(parsed)

	if err := validateParsed(request, parsed); err != nil {
		return ImportResult{}, fmt.Errorf("%w: invalid normalized connection", ErrInvalidConnectionString)
	}

	definition, err := i.newDefinition(ctx, request, parsed)
	if err != nil {
		return ImportResult{}, err
	}

	previous, isUpdated, err := i.existingDefinition(ctx, request.ID)
	if err != nil {
		i.deleteNew(ctx, definition.SecretRefs)
		return ImportResult{}, err
	}

	if err := i.definitions.Upsert(ctx, definition); err != nil {
		i.deleteNew(ctx, definition.SecretRefs)
		return ImportResult{}, fmt.Errorf("%w: saving definition", ErrImportUnavailable)
	}

	if _, err := i.registrar.Register(definition); err != nil {
		return ImportResult{}, fmt.Errorf("%w: registering definition", ErrImportUnavailable)
	}

	if isUpdated {
		i.deleteOld(ctx, request.ID, previous.SecretRefs)
	}

	return ImportResult{
		ID:                 request.ID,
		IsUpdated:          isUpdated,
		IsConnectionTested: false,
	}, nil
}

func (i *Importer) newDefinition(
	ctx context.Context,
	request ImportRequest,
	parsed ParsedConnection,
) (Definition, error) {
	refs, err := i.storeSecrets(ctx, parsed.Secrets)
	if err != nil {
		return Definition{}, err
	}

	definition := Definition{
		ID:         request.ID,
		Kind:       request.Kind,
		Settings:   cloneStrings(parsed.Settings),
		SecretRefs: refs,
	}
	if err := definition.Validate(); err != nil {
		i.deleteNew(ctx, refs)

		return Definition{}, fmt.Errorf(
			"%w: invalid normalized connection",
			ErrInvalidConnectionString,
		)
	}

	return definition, nil
}

func (i *Importer) existingDefinition(ctx context.Context, id ID) (Definition, bool, error) {
	definition, err := i.definitions.Lookup(ctx, id)
	if err == nil {
		return definition, true, nil
	}

	if errors.Is(err, ErrDefinitionNotFound) {
		return Definition{}, false, nil
	}

	return Definition{}, false, fmt.Errorf(
		"%w: loading existing definition",
		ErrImportUnavailable,
	)
}

func validateParsed(request ImportRequest, parsed ParsedConnection) error {
	refs := make(map[string]secret.Reference, len(parsed.Secrets))
	for name := range parsed.Secrets {
		refs[name] = "local://validation"
	}

	return (Definition{ID: request.ID, Kind: request.Kind, Settings: parsed.Settings, SecretRefs: refs}).Validate()
}

func (i *Importer) storeSecrets(ctx context.Context, secrets map[string][]byte) (map[string]secret.Reference, error) {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}

	sort.Strings(names)

	refs := make(map[string]secret.Reference, len(names))
	for _, name := range names {
		value := append([]byte(nil), secrets[name]...)
		ref, err := i.secrets.Store(ctx, value)
		zeroBytes(value)

		if err != nil {
			i.deleteNew(ctx, refs)
			return nil, fmt.Errorf("%w: storing secret", ErrImportUnavailable)
		}

		refs[name] = ref
	}

	return refs, nil
}

func (i *Importer) deleteNew(ctx context.Context, refs map[string]secret.Reference) {
	for _, ref := range refs {
		_ = i.secrets.Delete(ctx, ref)
	}
}

func (i *Importer) deleteOld(ctx context.Context, id ID, refs map[string]secret.Reference) {
	for _, ref := range refs {
		if err := i.secrets.Delete(ctx, ref); err != nil {
			i.warn(id, oldSecretCleanupFailed)
			break
		}
	}
}

func clearParsed(parsed ParsedConnection) {
	for _, value := range parsed.Secrets {
		zeroBytes(value)
	}
}
