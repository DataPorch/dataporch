package execution

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/adamraziv/dataporch/internal/access"
	"github.com/adamraziv/dataporch/internal/connection"
)

type recordingRelationalQueryExecutor struct {
	kind     connection.Kind
	requests []RelationalQueryExecutionRequest
	result   RelationalQueryResult
	err      error
}

func (e *recordingRelationalQueryExecutor) Kind() connection.Kind {
	return e.kind
}

func (e *recordingRelationalQueryExecutor) Query(
	_ context.Context,
	request RelationalQueryExecutionRequest,
) (RelationalQueryResult, error) {
	e.requests = append(e.requests, request)
	return e.result, e.err
}

func newQueryService(
	t *testing.T,
	definitions []connection.Definition,
	authorizer Authorizer,
	executors ...RelationalQueryExecutor,
) *Service {
	t.Helper()

	service, err := New(Dependencies{
		Sources:                  &sourceRegistryStub{definitions: definitions},
		Authorizer:               authorizer,
		MaxLimit:                 10,
		RelationalQueryExecutors: executors,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return service
}

func TestServiceQueryRelationalDatabaseValidation(t *testing.T) {
	t.Parallel()

	executor := &recordingRelationalQueryExecutor{kind: "postgres"}
	service := newQueryService(
		t,
		[]connection.Definition{{ID: "finance", Kind: "postgres"}},
		&recordingAuthorizer{},
		executor,
	)

	tests := []struct {
		name    string
		request RelationalQueryRequest
		want    error
	}{
		{name: "missing kind", request: RelationalQueryRequest{SourceID: "finance", Query: "SELECT 1"}, want: ErrInvalidRequest},
		{name: "unsupported kind", request: RelationalQueryRequest{Kind: "mysql", SourceID: "finance", Query: "SELECT 1"}, want: ErrInvalidRequest},
		{name: "missing source", request: RelationalQueryRequest{Kind: "postgres", Query: "SELECT 1"}, want: ErrInvalidRequest},
		{name: "missing query", request: RelationalQueryRequest{Kind: "postgres", SourceID: "finance"}, want: ErrInvalidRequest},
		{name: "blank query", request: RelationalQueryRequest{Kind: "postgres", SourceID: "finance", Query: " \n\t"}, want: ErrInvalidRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := service.QueryRelationalDatabase(t.Context(), test.request); !errors.Is(err, test.want) {
				t.Fatalf("QueryRelationalDatabase() error = %v, want %v", err, test.want)
			}
		})
	}

	if len(executor.requests) != 0 {
		t.Fatalf("executor requests = %d, want 0", len(executor.requests))
	}
}

func TestServiceQueryRelationalDatabaseAuthorizesBeforeLookup(t *testing.T) {
	t.Parallel()

	authorizer := &recordingAuthorizer{err: errors.New("policy denied")}
	executor := &recordingRelationalQueryExecutor{kind: "postgres"}
	service := newQueryService(t, nil, authorizer, executor)

	_, err := service.QueryRelationalDatabase(t.Context(), RelationalQueryRequest{
		Kind:     "postgres",
		SourceID: "missing",
		Query:    "SELECT 1",
	})
	if !errors.Is(err, ErrDataPorchAccessDenied) {
		t.Fatalf("error = %v, want ErrDataPorchAccessDenied", err)
	}

	want := []access.Request{{
		Action:   access.ActionQueryRelationalDatabase,
		Kind:     "postgres",
		SourceID: "missing",
	}}
	if !reflect.DeepEqual(authorizer.requests, want) {
		t.Fatalf("authorization requests = %#v, want %#v", authorizer.requests, want)
	}
}

func TestServiceQueryRelationalDatabaseReturnsSourceNotFound(t *testing.T) {
	t.Parallel()

	executor := &recordingRelationalQueryExecutor{kind: "postgres"}
	service := newQueryService(t, nil, &recordingAuthorizer{}, executor)

	_, err := service.QueryRelationalDatabase(t.Context(), RelationalQueryRequest{
		Kind:     "postgres",
		SourceID: "missing",
		Query:    "SELECT 1",
	})
	if !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("error = %v, want ErrSourceNotFound", err)
	}

	if len(executor.requests) != 0 {
		t.Fatalf("executor requests = %d, want 0", len(executor.requests))
	}
}

func TestServiceQueryRelationalDatabaseRejectsStoredKindMismatch(t *testing.T) {
	t.Parallel()

	executor := &recordingRelationalQueryExecutor{kind: "postgres"}
	service := newQueryService(
		t,
		[]connection.Definition{{ID: "finance", Kind: "mysql"}},
		&recordingAuthorizer{},
		executor,
	)

	_, err := service.QueryRelationalDatabase(t.Context(), RelationalQueryRequest{
		Kind:     "postgres",
		SourceID: "finance",
		Query:    "SELECT 1",
	})
	if !errors.Is(err, ErrSourceKindMismatch) {
		t.Fatalf("error = %v, want ErrSourceKindMismatch", err)
	}

	if len(executor.requests) != 0 {
		t.Fatalf("executor requests = %d, want 0", len(executor.requests))
	}
}

func TestServiceQueryRelationalDatabaseRoutesOriginalQuery(t *testing.T) {
	t.Parallel()

	executor := &recordingRelationalQueryExecutor{
		kind: "postgres",
		result: RelationalQueryResult{
			Kind:     "wrong",
			SourceID: "wrong",
		},
	}
	service := newQueryService(
		t,
		[]connection.Definition{{ID: "finance", Kind: "postgres"}},
		&recordingAuthorizer{},
		executor,
	)

	query := " \nSELECT 1; \t"

	result, err := service.QueryRelationalDatabase(t.Context(), RelationalQueryRequest{
		Kind:     "postgres",
		SourceID: "finance",
		Query:    query,
	})
	if err != nil {
		t.Fatalf("QueryRelationalDatabase() error = %v", err)
	}

	if len(executor.requests) != 1 || executor.requests[0].Query != query {
		t.Fatalf("executor request = %#v, want original query %q", executor.requests, query)
	}

	if result.Kind != "postgres" || result.SourceID != "finance" {
		t.Fatalf("result identity = %#v, want postgres/finance", result)
	}
}

func TestServiceQueryRelationalDatabaseSupportsSourcePolicySeam(t *testing.T) {
	t.Parallel()

	authorizer := &recordingAuthorizer{}
	financeExecutor := &recordingRelationalQueryExecutor{kind: "postgres"}
	service := newQueryService(
		t,
		[]connection.Definition{
			{ID: "finance", Kind: "postgres"},
			{ID: "payroll", Kind: "postgres"},
		},
		authorizer,
		financeExecutor,
	)

	authorizer.err = errors.New("payroll denied")

	_, err := service.QueryRelationalDatabase(t.Context(), RelationalQueryRequest{
		Kind:     "postgres",
		SourceID: "payroll",
		Query:    "SELECT 1",
	})
	if !errors.Is(err, ErrDataPorchAccessDenied) {
		t.Fatalf("denied error = %v, want ErrDataPorchAccessDenied", err)
	}

	if len(financeExecutor.requests) != 0 {
		t.Fatalf("denied executor requests = %d, want 0", len(financeExecutor.requests))
	}
}

func TestNewRejectsInvalidRelationalQueryExecutors(t *testing.T) {
	t.Parallel()

	validSources := &sourceRegistryStub{}
	validAuthorizer := &recordingAuthorizer{}
	validExecutor := &recordingRelationalQueryExecutor{kind: "postgres"}

	var typedNil *recordingRelationalQueryExecutor

	tests := []struct {
		name string
		deps Dependencies
	}{
		{
			name: "nil executor",
			deps: Dependencies{
				Sources:                  validSources,
				Authorizer:               validAuthorizer,
				MaxLimit:                 10,
				RelationalQueryExecutors: []RelationalQueryExecutor{nil},
			},
		},
		{
			name: "typed nil executor",
			deps: Dependencies{
				Sources:                  validSources,
				Authorizer:               validAuthorizer,
				MaxLimit:                 10,
				RelationalQueryExecutors: []RelationalQueryExecutor{typedNil},
			},
		},
		{
			name: "empty kind",
			deps: Dependencies{
				Sources:                  validSources,
				Authorizer:               validAuthorizer,
				MaxLimit:                 10,
				RelationalQueryExecutors: []RelationalQueryExecutor{&recordingRelationalQueryExecutor{}},
			},
		},
		{
			name: "duplicate kind",
			deps: Dependencies{
				Sources:                  validSources,
				Authorizer:               validAuthorizer,
				MaxLimit:                 10,
				RelationalQueryExecutors: []RelationalQueryExecutor{validExecutor, &recordingRelationalQueryExecutor{kind: "postgres"}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := New(test.deps); err == nil {
				t.Fatal("New() error = nil, want non-nil")
			}
		})
	}
}
