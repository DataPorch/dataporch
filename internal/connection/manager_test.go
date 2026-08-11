package connection

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/adamraziv/dataporch/internal/secret"
)

func TestManagerRegisterIsImmediatelyVisible(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(resolverStub{}, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	definition := testDefinition("finance", "local://secret-a")
	if err := manager.Register(definition); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	got, err := manager.Lookup("finance")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}

	if got.ID != "finance" {
		t.Errorf("Lookup().ID = %q, want finance", got.ID)
	}

	listed := manager.List()
	if len(listed) != 1 || listed[0].ID != "finance" {
		t.Fatalf("List() = %#v, want finance definition", listed)
	}
}

func TestManagerListReturnsSortedClones(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(resolverStub{}, []Definition{
		testDefinition("zeta", "local://secret-z"),
		testDefinition("alpha", "local://secret-a"),
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	first := manager.List()
	first[0].Settings["host"] = "mutated"
	second := manager.List()

	if got := []ID{second[0].ID, second[1].ID}; !slices.Equal(got, []ID{"alpha", "zeta"}) {
		t.Fatalf("List() ids = %v, want [alpha zeta]", got)
	}

	if second[0].Settings["host"] == "mutated" {
		t.Fatal("List() returned shared settings")
	}
}

func TestManagerPrepareResolvesAllNamedSecrets(t *testing.T) {
	t.Parallel()

	resolver := resolverStub{values: map[secret.Reference][]byte{"local://password": []byte("secret")}}

	manager, err := NewManager(resolver, []Definition{testDefinition("finance", "local://password")})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	resolved, err := manager.Prepare(t.Context(), "finance")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	if string(resolved.Secrets["password"]) != "secret" {
		t.Errorf("Prepare().Secrets = %q, want secret", resolved.Secrets["password"])
	}
}

func TestManagerPreparePreservesNotFoundClassification(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(resolverStub{}, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	_, err = manager.Prepare(t.Context(), "missing")
	for _, expected := range []error{ErrDatabaseUnavailable, ErrDatabaseNotFound} {
		if !errors.Is(err, expected) {
			t.Errorf("Prepare() error = %v, want %v", err, expected)
		}
	}
}

func TestManagerPreparePreservesContextClassification(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(resolverStub{}, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = manager.Prepare(ctx, "finance")
	for _, expected := range []error{ErrDatabaseUnavailable, context.Canceled} {
		if !errors.Is(err, expected) {
			t.Errorf("Prepare() error = %v, want %v", err, expected)
		}
	}
}

func TestManagerFailureForADoesNotAffectB(t *testing.T) {
	t.Parallel()

	resolver := resolverStub{
		values: map[secret.Reference][]byte{"local://secret-b": []byte("b")},
		errors: map[secret.Reference]error{
			"local://secret-a": errors.New("authentication failed"),
		},
	}

	manager, err := NewManager(resolver, []Definition{
		testDefinition("a", "local://secret-a"),
		testDefinition("b", "local://secret-b"),
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if _, err := manager.Prepare(context.Background(), "a"); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("Prepare(a) error = %v, want ErrDatabaseUnavailable", err)
	}

	if _, err := manager.Prepare(context.Background(), "b"); err != nil {
		t.Fatalf("Prepare(b) error = %v", err)
	}
}

type resolverStub struct {
	values map[secret.Reference][]byte
	errors map[secret.Reference]error
}

func (r resolverStub) Resolve(_ context.Context, ref secret.Reference) ([]byte, error) {
	if err := r.errors[ref]; err != nil {
		return nil, err
	}

	return append([]byte(nil), r.values[ref]...), nil
}

func testDefinition(id ID, ref secret.Reference) Definition {
	return Definition{
		ID:         id,
		Kind:       "postgres",
		Settings:   map[string]string{"host": "postgres.internal"},
		SecretRefs: map[string]secret.Reference{"password": ref},
	}
}
