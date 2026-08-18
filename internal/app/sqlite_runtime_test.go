//nolint:dupl // Adapter composition tests intentionally share the same contract.
package app

import (
	"testing"

	"github.com/adamraziv/dataporch/internal/connection/sqlite"
)

func TestNewSQLiteModule(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)

	module, err := newSQLiteModule(manager, validQueryPolicy())
	if err != nil {
		t.Fatalf("newSQLiteModule() error = %v", err)
	}

	assertRelationalModule(t, module, sqlite.Kind, func(runtime any) bool {
		_, ok := runtime.(*sqlite.Runtime)
		return ok
	}, "*sqlite.Runtime")

	if err := module.runtime.Close(t.Context()); err != nil {
		t.Fatalf("runtime.Close() error = %v", err)
	}
}

func TestNewSQLiteModuleRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)
	testRelationalModuleRejectsInvalidInputs(t, manager, newSQLiteModule)
}

func TestNewConstructsSQLiteRuntimeWithoutOpening(t *testing.T) {
	t.Parallel()

	testNewConstructsRelationalRuntimeWithoutOpening(t, newSQLiteModule, func(runtime any) bool {
		_, ok := runtime.(*sqlite.Runtime)
		return ok
	}, "*sqlite.Runtime")
}
