package connection

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/adamraziv/dataporch/internal/secret"
)

func TestImporterAddsNormalizedDefinitionWithoutAuthentication(t *testing.T) {
	t.Parallel()

	importer, dependencies := newImporter(t, ParsedConnection{
		Settings: map[string]string{"host": "postgres.internal", "username": "reader"},
		Secrets:  map[string][]byte{"password": []byte("canary")},
	})
	result, err := importer.Import(t.Context(), ImportRequest{
		ID:               "finance",
		Kind:             "postgres",
		ConnectionString: []byte("postgres://reader:canary@postgres.internal/finance"),
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Updated || result.ConnectionTested || result.ID != "finance" {
		t.Fatalf("Import() = %#v, want new untested finance result", result)
	}
	definition := dependencies.repository.definitions["finance"]
	if definition.Settings["host"] != "postgres.internal" || definition.SecretRefs["password"] == "" {
		t.Fatalf("saved definition = %#v, want normalized fields", definition)
	}
}

func TestImporterDeletesOldOwnedSecretAfterReplacement(t *testing.T) {
	t.Parallel()

	importer, dependencies := newImporter(t, ParsedConnection{
		Settings: map[string]string{"host": "new.internal"},
		Secrets:  map[string][]byte{"password": []byte("new")},
	})
	dependencies.repository.definitions["finance"] = Definition{
		ID:         "finance",
		Kind:       "postgres",
		Settings:   map[string]string{"host": "old.internal"},
		SecretRefs: map[string]secret.Reference{"password": "local://old"},
	}

	result, err := importer.Import(t.Context(), ImportRequest{
		ID:               "finance",
		Kind:             "postgres",
		ConnectionString: []byte("private"),
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if !result.Updated {
		t.Fatal("Import().Updated = false, want true")
	}
	if !dependencies.writer.deleted["local://old"] {
		t.Fatal("old secret was not deleted")
	}
}

func TestImporterCleansNewSecretsAfterPersistenceFailure(t *testing.T) {
	t.Parallel()

	importer, dependencies := newImporter(t, ParsedConnection{Secrets: map[string][]byte{"password": []byte("canary")}})
	dependencies.repository.upsertErr = errors.New("write failed")

	if _, err := importer.Import(t.Context(), ImportRequest{
		ID:               "finance",
		Kind:             "postgres",
		ConnectionString: []byte("private"),
	}); !errors.Is(err, ErrImportUnavailable) {
		t.Fatalf("Import() error = %v, want ErrImportUnavailable", err)
	}
	if !dependencies.writer.deleted["local://new-password"] {
		t.Fatal("new secret was not cleaned up")
	}
}

func TestImporterSanitizesParserError(t *testing.T) {
	t.Parallel()

	canary := "postgres://reader:canary@host/database"
	importer, _ := newImporter(t, ParsedConnection{})
	importer.adapters = adapterResolverStub{adapter: &adapterStub{kind: "postgres", err: errors.New(canary)}}

	_, err := importer.Import(t.Context(), ImportRequest{
		ID:               "finance",
		Kind:             "postgres",
		ConnectionString: []byte(canary),
	})
	if !errors.Is(err, ErrInvalidConnectionString) {
		t.Fatalf("Import() error = %v, want ErrInvalidConnectionString", err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("Import() error leaked connection string: %v", err)
	}
}

type importerDependencies struct {
	repository *definitionRepositoryStub
	writer     *secretWriterStub
}

func newImporter(t *testing.T, parsed ParsedConnection) (*Importer, importerDependencies) {
	t.Helper()

	repository := &definitionRepositoryStub{definitions: map[ID]Definition{}}
	writer := &secretWriterStub{deleted: map[secret.Reference]bool{}}
	importer, err := NewImporter(
		adapterResolverStub{adapter: &adapterStub{kind: "postgres", parsed: parsed}},
		writer,
		repository,
		registrarStub{},
		nil,
	)
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}
	return importer, importerDependencies{repository: repository, writer: writer}
}

type adapterResolverStub struct{ adapter Adapter }

func (s adapterResolverStub) Resolve(Kind) (Adapter, error) { return s.adapter, nil }

type secretWriterStub struct {
	deleted map[secret.Reference]bool
}

func (s *secretWriterStub) Store(_ context.Context, value []byte) (secret.Reference, error) {
	return secret.Reference("local://new-password"), nil
}

func (s *secretWriterStub) Delete(_ context.Context, ref secret.Reference) error {
	s.deleted[ref] = true
	return nil
}

type definitionRepositoryStub struct {
	definitions map[ID]Definition
	upsertErr   error
}

func (s *definitionRepositoryStub) Lookup(_ context.Context, id ID) (Definition, error) {
	definition, exists := s.definitions[id]
	if !exists {
		return Definition{}, ErrDefinitionNotFound
	}
	return definition.Clone(), nil
}

func (s *definitionRepositoryStub) Upsert(_ context.Context, definition Definition) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.definitions[definition.ID] = definition.Clone()
	return nil
}

type registrarStub struct{}

func (registrarStub) Register(Definition) error { return nil }
