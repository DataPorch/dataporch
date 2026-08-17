package app

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/adamraziv/dataporch/internal/connection"
)

func TestReplacementRegistrarRoutesInvalidationByKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		result       connection.RegistrationResult
		nextKind     connection.Kind
		runtimeKinds []connection.Kind
		expected     []string
	}{
		{
			name:         "new definition invalidates current owner",
			nextKind:     "mysql",
			runtimeKinds: []connection.Kind{"mysql"},
			expected:     []string{"register", "mysql:finance"},
		},
		{
			name: "same kind invalidates once",
			result: connection.RegistrationResult{
				Previous: connection.Definition{ID: "finance", Kind: "postgres"},
				Replaced: true,
			},
			nextKind:     "postgres",
			runtimeKinds: []connection.Kind{"postgres"},
			expected:     []string{"register", "postgres:finance"},
		},
		{
			name: "cross kind invalidates former then current",
			result: connection.RegistrationResult{
				Previous: connection.Definition{ID: "finance", Kind: "postgres"},
				Replaced: true,
			},
			nextKind:     "mysql",
			runtimeKinds: []connection.Kind{"postgres", "mysql"},
			expected:     []string{"register", "postgres:finance", "mysql:finance"},
		},
		{
			name: "missing former owner still invalidates current",
			result: connection.RegistrationResult{
				Previous: connection.Definition{ID: "finance", Kind: "sqlite"},
				Replaced: true,
			},
			nextKind:     "mysql",
			runtimeKinds: []connection.Kind{"mysql"},
			expected:     []string{"register", "mysql:finance"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			events := make([]string, 0, len(test.expected))
			underlying := &definitionRegistrarStub{result: test.result, events: &events}

			registrar, err := newReplacementRegistrar(
				underlying,
				runtimeIndex(&events, test.runtimeKinds...),
			)
			if err != nil {
				t.Fatalf("newReplacementRegistrar() error = %v", err)
			}

			definition := connection.Definition{ID: "finance", Kind: test.nextKind}

			result, err := registrar.Register(definition)
			if err != nil {
				t.Fatalf("Register() error = %v", err)
			}

			if !reflect.DeepEqual(result, test.result) {
				t.Fatalf("Register() result = %#v, want %#v", result, test.result)
			}

			if !slices.Equal(events, test.expected) {
				t.Fatalf("events = %v, want %v", events, test.expected)
			}
		})
	}
}

func TestReplacementRegistrarRoutesFailureWithoutInvalidation(t *testing.T) {
	t.Parallel()

	registrationErr := errors.New("registration failed")
	events := make([]string, 0, 1)

	registrar, err := newReplacementRegistrar(
		&definitionRegistrarStub{err: registrationErr, events: &events},
		runtimeIndex(&events, "postgres", "mysql"),
	)
	if err != nil {
		t.Fatalf("newReplacementRegistrar() error = %v", err)
	}

	result, err := registrar.Register(connection.Definition{ID: "finance", Kind: "postgres"})
	if !errors.Is(err, registrationErr) {
		t.Fatalf("Register() error = %v, want %v", err, registrationErr)
	}

	if !reflect.DeepEqual(result, connection.RegistrationResult{}) {
		t.Fatalf("Register() result = %#v, want zero result", result)
	}

	if !slices.Equal(events, []string{"register"}) {
		t.Fatalf("events = %v, want registration only", events)
	}
}

func TestNewReplacementRegistrarRejectsMissingRegistrar(t *testing.T) {
	t.Parallel()

	if _, err := newReplacementRegistrar(nil, map[connection.Kind]runtimeLifecycle{}); !errors.Is(err, errDefinitionRegistrarRequired) {
		t.Fatalf("newReplacementRegistrar() error = %v, want %v", err, errDefinitionRegistrarRequired)
	}
}

type definitionRegistrarStub struct {
	result connection.RegistrationResult
	err    error
	events *[]string
}

func (r *definitionRegistrarStub) Register(
	connection.Definition,
) (connection.RegistrationResult, error) {
	if r.events != nil {
		*r.events = append(*r.events, "register")
	}

	if r.err != nil {
		return connection.RegistrationResult{}, r.err
	}

	return r.result, nil
}

func runtimeIndex(
	events *[]string,
	kinds ...connection.Kind,
) map[connection.Kind]runtimeLifecycle {
	runtimes := make(map[connection.Kind]runtimeLifecycle, len(kinds))
	for _, kind := range kinds {
		runtimes[kind] = &relationalRuntimeStub{
			name:   string(kind),
			events: events,
		}
	}

	return runtimes
}
