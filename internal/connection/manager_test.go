package connection

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
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
	if _, err := manager.Register(definition); err != nil {
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

func TestManagerRegisterReturnsAtomicTransition(t *testing.T) {
	t.Parallel()

	initial := testDefinition("finance", "local://secret-a")
	initial.Kind = "postgres"
	initial.Settings["host"] = "old.internal"

	manager, err := NewManager(resolverStub{}, []Definition{initial})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	next := testDefinition("finance", "local://secret-b")
	next.Kind = "mysql"
	next.Settings["host"] = "new.internal"

	result, err := manager.Register(next)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if !result.Replaced {
		t.Fatal("Register().Replaced = false, want true")
	}

	if result.Previous.Kind != "postgres" || result.Previous.Settings["host"] != "old.internal" {
		t.Fatalf("Register().Previous = %#v, want old postgres definition", result.Previous)
	}

	next.Settings["host"] = "caller-mutated"

	got, err := manager.Lookup("finance")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}

	if got.Settings["host"] != "new.internal" {
		t.Fatalf("Lookup().Settings[host] = %q, want new.internal", got.Settings["host"])
	}
}

func TestManagerRegisterNewAndInvalidResults(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(resolverStub{}, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	result, err := manager.Register(testDefinition("finance", "local://secret-a"))
	if err != nil {
		t.Fatalf("Register(new) error = %v", err)
	}

	if result.Replaced || result.Previous.ID != "" {
		t.Fatalf("Register(new) result = %#v, want zero previous", result)
	}

	result, err = manager.Register(Definition{ID: "finance"})
	if !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("Register(invalid) error = %v, want ErrInvalidDefinition", err)
	}

	resultIsZero := !result.Replaced &&
		result.Previous.ID == "" &&
		result.Previous.Kind == "" &&
		result.Previous.Settings == nil &&
		result.Previous.SecretRefs == nil
	if !resultIsZero {
		t.Fatalf("Register(invalid) result = %#v, want zero", result)
	}

	got, err := manager.Lookup("finance")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}

	if got.Kind != "postgres" {
		t.Fatalf("Lookup().Kind = %q, want postgres", got.Kind)
	}
}

func TestManagerConcurrentRegistrationsReturnLinearizableTransitions(t *testing.T) {
	t.Parallel()

	const registrations = 32

	initial := testDefinition("finance", "local://secret-0")
	initial.Settings["host"] = "0"

	manager, err := NewManager(resolverStub{}, []Definition{initial})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	type transition struct {
		current Definition
		result  RegistrationResult
		err     error
	}

	start := make(chan struct{})
	transitions := make(chan transition, registrations)

	var group sync.WaitGroup

	for index := 1; index <= registrations; index++ {
		current := testDefinition("finance", secret.Reference("local://secret-"+strconv.Itoa(index)))
		current.Settings["host"] = strconv.Itoa(index)

		group.Go(func() {
			<-start

			result, err := manager.Register(current)
			transitions <- transition{current: current, result: result, err: err}
		})
	}

	close(start)
	group.Wait()
	close(transitions)

	nextByPrevious := make(map[string]string, registrations)

	for transition := range transitions {
		if transition.err != nil {
			t.Fatalf("Register() error = %v", transition.err)
		}

		if !transition.result.Replaced {
			t.Fatal("concurrent Register().Replaced = false, want true")
		}

		previous := transition.result.Previous.Settings["host"]
		current := transition.current.Settings["host"]

		if _, exists := nextByPrevious[previous]; exists {
			t.Fatalf("duplicate previous host %q", previous)
		}

		nextByPrevious[previous] = current
	}

	seen := map[string]bool{"0": true}

	cursor := "0"
	for range registrations {
		next, exists := nextByPrevious[cursor]
		if !exists {
			t.Fatalf("transition chain stops at %q", cursor)
		}

		if seen[next] {
			t.Fatalf("transition chain contains cycle at %q", next)
		}

		seen[next] = true
		cursor = next
	}

	got, err := manager.Lookup("finance")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}

	if got.Settings["host"] != cursor || len(seen) != registrations+1 {
		t.Fatalf("final transition = %q, lookup = %q, visited = %d", cursor, got.Settings["host"], len(seen))
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
