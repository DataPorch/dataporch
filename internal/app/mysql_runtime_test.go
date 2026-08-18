package app

import (
	"testing"

	"github.com/adamraziv/dataporch/internal/connection/mysql"
)

func TestNewMySQLModule(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)
	module, err := newMySQLModule(manager, validQueryPolicy())
	if err != nil {
		t.Fatalf("newMySQLModule() error = %v", err)
	}

	assertRelationalModule(t, module, mysql.Kind, func(runtime any) bool {
		_, ok := runtime.(*mysql.Opener)
		return ok
	}, "*mysql.Opener")

	if err := module.runtime.Close(t.Context()); err != nil {
		t.Fatalf("runtime.Close() error = %v", err)
	}
}

func TestNewMySQLModuleRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	manager := newRelationalTestManager(t)
	testRelationalModuleRejectsInvalidInputs(t, manager, newMySQLModule)
}

func TestNewConstructsMySQLRuntimeWithoutOpening(t *testing.T) {
	t.Parallel()

	testNewConstructsRelationalRuntimeWithoutOpening(t, newMySQLModule, func(runtime any) bool {
		_, ok := runtime.(*mysql.Opener)
		return ok
	}, "*mysql.Opener")
}
