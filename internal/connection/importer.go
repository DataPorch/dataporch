package connection

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/adamraziv/dataporch/internal/secret"
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
	ID               ID
	Updated          bool
	ConnectionTested bool
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
	Register(Definition) error
}

type AdapterResolver interface {
	Resolve(Kind) (Adapter, error)
}

type CleanupWarning func(databaseID ID, category string)

type Importer struct {
	adapters    AdapterResolver
	secrets     SecretWriter
	definitions DefinitionRepository
	registrar   DefinitionRegistrar
	warn        CleanupWarning
}

func NewImporter(
	adapters AdapterResolver,
	secrets SecretWriter,
	definitions DefinitionRepository,
	registrar DefinitionRegistrar,
	warn CleanupWarning,
) (*Importer, error) {
	if adapters == nil || secrets == nil || definitions == nil || registrar == nil {
		return nil, ErrImportUnavailable
	}
	if warn == nil {
		warn = func(ID, string) {}
	}
	return &Importer{
		adapters:    adapters,
		secrets:     secrets,
		definitions: definitions,
		registrar:   registrar,
		warn:        warn,
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
	defer clear(input)
	parsed, err := adapter.ParseConnectionString(input)
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w: %s", ErrInvalidConnectionString, request.Kind)
	}
	defer clearParsed(parsed)
	if err := validateParsed(request, parsed); err != nil {
		return ImportResult{}, fmt.Errorf("%w: invalid normalized connection", ErrInvalidConnectionString)
	}

	refs, err := i.storeSecrets(ctx, parsed.Secrets)
	if err != nil {
		return ImportResult{}, err
	}
	definition := Definition{ID: request.ID, Kind: request.Kind, Settings: cloneStrings(parsed.Settings), SecretRefs: refs}
	if err := definition.Validate(); err != nil {
		i.deleteNew(ctx, refs)
		return ImportResult{}, fmt.Errorf("%w: invalid normalized connection", ErrInvalidConnectionString)
	}

	previous, lookupErr := i.definitions.Lookup(ctx, request.ID)
	updated := lookupErr == nil
	if lookupErr != nil && !errors.Is(lookupErr, ErrDefinitionNotFound) {
		i.deleteNew(ctx, refs)
		return ImportResult{}, fmt.Errorf("%w: loading existing definition", ErrImportUnavailable)
	}
	if err := i.definitions.Upsert(ctx, definition); err != nil {
		i.deleteNew(ctx, refs)
		return ImportResult{}, fmt.Errorf("%w: saving definition", ErrImportUnavailable)
	}
	if err := i.registrar.Register(definition); err != nil {
		return ImportResult{}, fmt.Errorf("%w: registering definition", ErrImportUnavailable)
	}
	if updated {
		for _, ref := range previous.SecretRefs {
			if err := i.secrets.Delete(ctx, ref); err != nil {
				i.warn(request.ID, oldSecretCleanupFailed)
				break
			}
		}
	}
	return ImportResult{ID: request.ID, Updated: updated, ConnectionTested: false}, nil
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
		clear(value)
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

func clearParsed(parsed ParsedConnection) {
	for _, value := range parsed.Secrets {
		clear(value)
	}
}
