package app

import (
	"bytes"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/connection/sqlite"
	"github.com/adamraziv/dataporch/internal/execution"
)

func TestNewSQLiteModule(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)

	module, err := newSQLiteModule(manager, validQueryPolicy())
	if err != nil {
		t.Fatalf("newSQLiteModule() error = %v", err)
	}

	if module.adapter.Kind() != sqlite.Kind {
		t.Fatalf("adapter kind = %q, want %q", module.adapter.Kind(), sqlite.Kind)
	}

	if module.discoverer.Kind() != sqlite.Kind || module.queryExecutor.Kind() != sqlite.Kind {
		t.Fatal("SQLite execution components disagree with adapter kind")
	}

	if _, ok := module.runtime.(*sqlite.Runtime); !ok {
		t.Fatalf("runtime type = %T, want *sqlite.Runtime", module.runtime)
	}

	if err := module.runtime.Close(t.Context()); err != nil {
		t.Fatalf("runtime.Close() error = %v", err)
	}
}

func TestNewSQLiteModuleRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)
	tests := []struct {
		name    string
		manager *connection.Manager
		policy  queryPolicy
	}{
		{name: "nil manager", manager: nil, policy: validQueryPolicy()},
		{name: "missing timeout", manager: manager, policy: queryPolicy{
			responseByteLimit: 1024,
			truncationEnabled: true,
			rowLimit:          100,
		}},
		{name: "missing response byte limit", manager: manager, policy: queryPolicy{
			timeout:           time.Second,
			truncationEnabled: true,
			rowLimit:          100,
		}},
		{name: "missing truncation row limit", manager: manager, policy: queryPolicy{
			timeout:           time.Second,
			responseByteLimit: 1024,
			truncationEnabled: true,
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			module, err := newSQLiteModule(test.manager, test.policy)
			if err == nil {
				t.Fatal("newSQLiteModule() error = nil, want error")
			}

			if !reflect.DeepEqual(module, relationalModule{}) {
				t.Fatalf("newSQLiteModule() module = %#v, want zero module", module)
			}
		})
	}
}

func TestNewConstructsSQLiteRuntimeWithoutOpening(t *testing.T) {
	t.Parallel()

	application, err := newWithDependencies(
		testConfigFor(t),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		appDependencies{
			relationalModuleFactories: []relationalModuleFactory{newSQLiteModule},
			newExecutionService:       execution.New,
		},
	)
	if err != nil {
		t.Fatalf("newWithDependencies() error = %v", err)
	}

	if len(application.runtimes) != 1 {
		t.Fatalf("application runtimes = %d, want 1", len(application.runtimes))
	}

	if _, ok := application.runtimes[0].(*sqlite.Runtime); !ok {
		t.Fatalf("application runtime type = %T, want *sqlite.Runtime", application.runtimes[0])
	}
}
