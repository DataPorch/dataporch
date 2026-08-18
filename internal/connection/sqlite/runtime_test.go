package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

func TestRuntimeRejectsNilPreparer(t *testing.T) {
	t.Parallel()

	if _, err := NewRuntime(nil); err == nil {
		t.Fatal("NewRuntime(nil) error = nil, want error")
	}
}

func TestRuntimeOpenValidatesResolvedDefinition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		definition connection.ResolvedDefinition
	}{
		{
			name: "wrong id",
			definition: connection.ResolvedDefinition{
				ID: "other", Kind: Kind, Secrets: map[string][]byte{secretPath: []byte("/tmp/db")},
			},
		},
		{
			name: "wrong kind",
			definition: connection.ResolvedDefinition{
				ID: "source", Kind: "postgres", Secrets: map[string][]byte{secretPath: []byte("/tmp/db")},
			},
		},
		{
			name:       "missing path",
			definition: connection.ResolvedDefinition{ID: "source", Kind: Kind, Secrets: map[string][]byte{}},
		},
		{
			name: "extra secret",
			definition: connection.ResolvedDefinition{
				ID: "source", Kind: Kind,
				Secrets: map[string][]byte{secretPath: []byte("/tmp/db"), "other": []byte("secret")},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			preparer := &runtimePreparer{definition: test.definition}
			opener := &runtimeOpener{}
			runtime, err := newRuntime(preparer, opener.open)
			if err != nil {
				t.Fatalf("newRuntime() error = %v", err)
			}

			_, err = runtime.open(t.Context(), "source", accessModeQuery)
			if err == nil {
				t.Fatal("Runtime.open() error = nil, want invalid definition error")
			}
			if opener.calls() != 0 {
				t.Fatalf("physical opener calls = %d, want 0", opener.calls())
			}
			if !runtimeSecretsCleared(preparer.lastSecrets) {
				t.Fatalf("resolved secret bytes were not cleared: %#v", preparer.lastSecrets)
			}
		})
	}
}

func TestRuntimeOpenClearsSecretsAndClosesPhysicalClientOnce(t *testing.T) {
	t.Parallel()

	definition := connection.ResolvedDefinition{
		ID:       "source",
		Kind:     Kind,
		Settings: map[string]string{"ignored": "setting"},
		Secrets:  map[string][]byte{secretPath: []byte("/tmp/dataporch.db")},
	}
	preparer := &runtimePreparer{definition: definition}
	opener := &runtimeOpener{}
	runtime, err := newRuntime(preparer, opener.open)
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}

	client, err := runtime.open(t.Context(), "source", accessModeDiscovery)
	if err != nil {
		t.Fatalf("Runtime.open() error = %v", err)
	}
	if !runtimeSecretsCleared(preparer.lastSecrets) {
		t.Fatalf("resolved secret bytes were not cleared: %#v", preparer.lastSecrets)
	}

	if err := client.close(); err != nil {
		t.Fatalf("client.close() error = %v", err)
	}
	if err := client.close(); err != nil {
		t.Fatalf("repeated client.close() error = %v", err)
	}
	if got := opener.lastCloseCount(); got != 1 {
		t.Fatalf("physical close count = %d, want 1", got)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("Runtime.Close() error = %v", err)
	}
}

func TestRuntimeReleaseRemovesInactiveEntry(t *testing.T) {
	t.Parallel()

	preparer := &runtimePreparer{definition: connection.ResolvedDefinition{
		ID:   "source",
		Kind: Kind,
		Secrets: map[string][]byte{
			secretPath: []byte("/tmp/source.db"),
		},
	}}
	opener := &runtimeOpener{}

	runtime, err := newRuntime(preparer, opener.open)
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}

	opened, err := runtime.open(t.Context(), "source", accessModeQuery)
	if err != nil {
		t.Fatalf("Runtime.open() error = %v", err)
	}

	if _, exists := runtime.entries["source"]; !exists {
		t.Fatal("active source entry is missing")
	}

	if err := opened.close(); err != nil {
		t.Fatalf("client.close() error = %v", err)
	}

	if _, exists := runtime.entries["source"]; exists {
		t.Fatal("inactive source entry remains retained")
	}

	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("Runtime.Close() error = %v", err)
	}
}

func TestRuntimeInvalidationDetachesOnlyCurrentEntry(t *testing.T) {
	t.Parallel()

	preparer := &runtimePreparer{definition: connection.ResolvedDefinition{
		ID: "source", Kind: Kind, Secrets: map[string][]byte{secretPath: []byte("/tmp/source.db")},
	}}
	opener := &runtimeOpener{}
	runtime, err := newRuntime(preparer, opener.open)
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}

	first, err := runtime.open(t.Context(), "source", accessModeQuery)
	if err != nil {
		t.Fatalf("first Runtime.open() error = %v", err)
	}
	other, err := runtime.open(t.Context(), "other", accessModeQuery)
	if err != nil {
		t.Fatalf("other Runtime.open() error = %v", err)
	}

	runtime.Invalidate("source")
	if _, exists := runtime.entries["source"]; exists {
		t.Fatal("invalidated source remains the current logical entry")
	}
	if _, exists := runtime.entries["other"]; !exists {
		t.Fatal("invalidation detached an unrelated logical entry")
	}

	preparer.definition.Secrets[secretPath] = []byte("/tmp/replacement.db")
	replacement, err := runtime.open(t.Context(), "source", accessModeQuery)
	if err != nil {
		t.Fatalf("replacement Runtime.open() error = %v", err)
	}

	replacementEntry := runtime.entries["source"]
	if replacementEntry == nil {
		t.Fatal("replacement logical entry is missing")
	}

	if err := first.close(); err != nil {
		t.Fatalf("first client.close() error = %v", err)
	}

	if got := runtime.entries["source"]; got != replacementEntry {
		t.Fatalf("stale release changed replacement entry: got %p, want %p", got, replacementEntry)
	}

	for _, client := range []*client{other, replacement} {
		if err := client.close(); err != nil {
			t.Fatalf("client.close() error = %v", err)
		}
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("Runtime.Close() error = %v", err)
	}
}

func TestRuntimeCloseWaitsAndRejectsNewWork(t *testing.T) {
	t.Parallel()

	preparer := &runtimePreparer{definition: connection.ResolvedDefinition{
		ID: "source", Kind: Kind, Secrets: map[string][]byte{secretPath: []byte("/tmp/source.db")},
	}}
	opener := &runtimeOpener{}
	runtime, err := newRuntime(preparer, opener.open)
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}
	active, err := runtime.open(t.Context(), "source", accessModeQuery)
	if err != nil {
		t.Fatalf("Runtime.open() error = %v", err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runtime.Close(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Runtime.Close() error = %v, want context.Canceled", err)
	}
	if _, err := runtime.open(t.Context(), "source", accessModeQuery); err == nil {
		t.Fatal("Runtime.open() after close error = nil, want rejection")
	}

	done := make(chan error, 1)
	go func() { done <- runtime.Close(t.Context()) }()
	select {
	case err := <-done:
		t.Fatalf("Runtime.Close() returned while active: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	if err := active.close(); err != nil {
		t.Fatalf("active.close() error = %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Runtime.Close() after drain error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Runtime.Close() did not return after active client closed")
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("repeated Runtime.Close() error = %v", err)
	}
}

func TestRuntimeConcurrentOpensUseIndependentPhysicalClients(t *testing.T) {
	t.Parallel()

	preparer := &runtimePreparer{definition: connection.ResolvedDefinition{
		ID: "source", Kind: Kind, Secrets: map[string][]byte{secretPath: []byte("/tmp/source.db")},
	}}
	opener := &runtimeOpener{}
	runtime, err := newRuntime(preparer, opener.open)
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}

	const count = 16
	clients := make([]*client, count)
	errCh := make(chan error, count)
	var group sync.WaitGroup
	for index := range clients {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			opened, err := runtime.open(t.Context(), "source", accessModeQuery)
			clients[index] = opened
			errCh <- err
		}(index)
	}
	group.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent Runtime.open() error = %v", err)
		}
	}
	if got := opener.calls(); got != count {
		t.Fatalf("physical opener calls = %d, want %d", got, count)
	}

	for _, client := range clients {
		if err := client.close(); err != nil {
			t.Fatalf("client.close() error = %v", err)
		}
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("Runtime.Close() error = %v", err)
	}
}

type runtimePreparer struct {
	mu          sync.Mutex
	definition  connection.ResolvedDefinition
	err         error
	lastSecrets map[string][]byte
}

func (p *runtimePreparer) Prepare(_ context.Context, id connection.ID) (connection.ResolvedDefinition, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	definition := p.definition.Clone()
	if definition.ID == "source" && id != "source" {
		definition.ID = id
	}
	p.lastSecrets = definition.Secrets
	return definition, p.err
}

type runtimeOpener struct {
	mu          sync.Mutex
	connections []*runtimeRawConnection
}

func (o *runtimeOpener) open(_ context.Context, _ string, _ accessMode) (rawConnection, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	connection := &runtimeRawConnection{}
	o.connections = append(o.connections, connection)
	return connection, nil
}

func (o *runtimeOpener) calls() int {
	o.mu.Lock()
	defer o.mu.Unlock()

	return len(o.connections)
}

func (o *runtimeOpener) lastCloseCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()

	if len(o.connections) == 0 {
		return 0
	}
	return o.connections[len(o.connections)-1].closeCount
}

type runtimeRawConnection struct {
	mu         sync.Mutex
	closeCount int
}

func (c *runtimeRawConnection) Close() error {
	c.mu.Lock()
	c.closeCount++
	c.mu.Unlock()
	return nil
}

func (*runtimeRawConnection) Config(sqlite3.DBConfig, ...bool) (bool, error) { return false, nil }
func (*runtimeRawConnection) Exec(string) error                              { return nil }
func (*runtimeRawConnection) Prepare(string) (statement, string, error) {
	return &runtimeStatement{}, "", nil
}
func (*runtimeRawConnection) SetAuthorizer(func(sqlite3.AuthorizerActionCode, string, string, string, string) sqlite3.AuthorizerReturnCode) error {
	return nil
}
func (*runtimeRawConnection) SetInterrupt(ctx context.Context) context.Context { return ctx }

type runtimeStatement struct{}

func (*runtimeStatement) BindCount() int                  { return 0 }
func (*runtimeStatement) BindInt64(int, int64) error      { return nil }
func (*runtimeStatement) BindText(int, string) error      { return nil }
func (*runtimeStatement) Close() error                    { return nil }
func (*runtimeStatement) ColumnCount() int                { return 1 }
func (*runtimeStatement) ColumnDeclType(int) string       { return "" }
func (*runtimeStatement) ColumnFloat(int) float64         { return 0 }
func (*runtimeStatement) ColumnInt64(int) int64           { return 0 }
func (*runtimeStatement) ColumnName(int) string           { return "" }
func (*runtimeStatement) ColumnRawBlob(int) []byte        { return nil }
func (*runtimeStatement) ColumnRawText(int) []byte        { return nil }
func (*runtimeStatement) ColumnText(int) string           { return "" }
func (*runtimeStatement) ColumnType(int) sqlite3.Datatype { return sqlite3.INTEGER }
func (*runtimeStatement) Err() error                      { return nil }
func (*runtimeStatement) Step() bool                      { return false }

func runtimeSecretsCleared(secrets map[string][]byte) bool {
	for _, value := range secrets {
		for _, byteValue := range value {
			if byteValue != 0 {
				return false
			}
		}
	}
	return true
}
