package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/execution"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

func TestNewDiscovererValidatesMetadataTimeout(t *testing.T) {
	t.Parallel()

	runtime := &deadlineDiscoveryRuntime{}
	if _, err := newDiscoverer(runtime, 0); !errors.Is(err, errMetadataQueryTimeoutRequired) {
		t.Fatalf("newDiscoverer(timeout 0) error = %v, want timeout validation", err)
	}
}

func TestDiscovererStartsMetadataTimeoutAfterOpen(t *testing.T) {
	t.Parallel()

	raw := &deadlineDiscoveryRawConnection{}
	runtime := &deadlineDiscoveryRuntime{raw: raw}
	timeout := 100 * time.Millisecond
	discoverer, err := newDiscoverer(runtime, timeout)
	if err != nil {
		t.Fatalf("newDiscoverer() error = %v", err)
	}

	_, err = discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID: "source",
		Schema:   "main",
		Limit:    1,
	})
	if err != nil {
		t.Fatalf("ListTables() error = %v", err)
	}

	if runtime.openDeadline {
		t.Fatal("runtime open received metadata deadline; want caller context")
	}

	remaining := time.Until(raw.interruptDeadline)
	if raw.interruptDeadline.IsZero() || remaining <= 0 || remaining > timeout {
		t.Fatalf("catalog interrupt deadline = %v, remaining %s; want within %s", raw.interruptDeadline, remaining, timeout)
	}
}

func TestDiscovererProjectsCatalogFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		prepareErr error
		stepErr    error
		closeErr   error
		category   execution.ErrorCategory
	}{
		{
			name:       "prepare",
			prepareErr: fmt.Errorf("catalog path /private/secret.db: %w", sqlite3.ERROR),
			category:   execution.ErrorCategoryDatabaseError,
		},
		{
			name:     "step",
			stepErr:  fmt.Errorf("catalog path /private/secret.db: %w", sqlite3.ERROR),
			category: execution.ErrorCategoryDatabaseError,
		},
		{
			name:     "close",
			closeErr: fmt.Errorf("catalog path /private/secret.db: %w", sqlite3.IOERR_CLOSE),
			category: execution.ErrorCategoryDatabaseUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := &discoveryErrorRawConnection{
				prepareErr: test.prepareErr,
				stepErr:    test.stepErr,
				closeErr:   test.closeErr,
			}
			discoverer, err := newDiscoverer(&discoveryErrorRuntime{raw: raw}, time.Second)
			if err != nil {
				t.Fatalf("newDiscoverer() error = %v", err)
			}

			_, err = discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
				SourceID: "source",
				Schema:   "main",
				Limit:    1,
			})
			if err == nil {
				t.Fatal("ListTables() error = nil, want projected catalog failure")
			}
			failure := databaseFailureFromError(t, err)
			if failure.Category != test.category {
				t.Fatalf("failure category = %q, want %q", failure.Category, test.category)
			}
			if strings.Contains(err.Error(), "/private/secret.db") {
				t.Fatalf("catalog path leaked from projected error: %v", err)
			}
		})
	}
}

func TestDiscovererClassifiesMetadataTimeout(t *testing.T) {
	t.Parallel()

	raw := &discoveryErrorRawConnection{
		stepWait: true,
	}
	discoverer, err := newDiscoverer(&discoveryErrorRuntime{raw: raw}, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("newDiscoverer() error = %v", err)
	}

	_, err = discoverer.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID: "source",
		Schema:   "main",
		Limit:    1,
	})
	if !errors.Is(err, execution.ErrQueryTimeout) {
		t.Fatalf("ListTables() error = %v, want metadata timeout", err)
	}
}

type deadlineDiscoveryRuntime struct {
	raw          *deadlineDiscoveryRawConnection
	openDeadline bool
}

func (r *deadlineDiscoveryRuntime) open(ctx context.Context, _ connection.ID, _ accessMode) (*client, error) {
	_, r.openDeadline = ctx.Deadline()
	return &client{
		conn:    r.raw,
		release: func() {},
	}, nil
}

type deadlineDiscoveryRawConnection struct {
	runtimeRawConnection
	interruptDeadline time.Time
}

func (c *deadlineDiscoveryRawConnection) SetInterrupt(ctx context.Context) context.Context {
	c.interruptDeadline, _ = ctx.Deadline()
	return ctx
}

type discoveryErrorRuntime struct {
	raw *discoveryErrorRawConnection
}

func (r *discoveryErrorRuntime) open(ctx context.Context, _ connection.ID, _ accessMode) (*client, error) {
	r.raw.SetInterrupt(ctx)
	return &client{
		conn:    r.raw,
		release: func() {},
	}, nil
}

type discoveryErrorRawConnection struct {
	interruptDone <-chan struct{}
	prepareErr    error
	stepErr       error
	closeErr      error
	stepWait      bool
}

func (c *discoveryErrorRawConnection) Close() error {
	return nil
}

func (*discoveryErrorRawConnection) Config(sqlite3.DBConfig, ...bool) (bool, error) {
	return true, nil
}

func (*discoveryErrorRawConnection) Exec(string) error {
	return nil
}

func (c *discoveryErrorRawConnection) Prepare(string) (statement, string, error) {
	if c.prepareErr != nil {
		return nil, "", c.prepareErr
	}
	return &discoveryErrorStatement{
		interruptDone: c.interruptDone,
		stepErr:       c.stepErr,
		closeErr:      c.closeErr,
		stepWait:      c.stepWait,
	}, "", nil
}

func (*discoveryErrorRawConnection) SetAuthorizer(func(sqlite3.AuthorizerActionCode, string, string, string, string) sqlite3.AuthorizerReturnCode) error {
	return nil
}

func (c *discoveryErrorRawConnection) SetInterrupt(ctx context.Context) context.Context {
	c.interruptDone = ctx.Done()
	return nil
}

type discoveryErrorStatement struct {
	interruptDone <-chan struct{}
	stepErr       error
	closeErr      error
	stepWait      bool
}

func (*discoveryErrorStatement) BindCount() int {
	return 0
}

func (*discoveryErrorStatement) BindInt64(int, int64) error {
	return nil
}

func (*discoveryErrorStatement) BindText(int, string) error {
	return nil
}

func (s *discoveryErrorStatement) Close() error {
	return s.closeErr
}

func (*discoveryErrorStatement) ColumnCount() int {
	return 1
}

func (*discoveryErrorStatement) ColumnDeclType(int) string {
	return ""
}

func (*discoveryErrorStatement) ColumnFloat(int) float64 {
	return 0
}

func (*discoveryErrorStatement) ColumnInt64(int) int64 {
	return 0
}

func (*discoveryErrorStatement) ColumnName(int) string {
	return ""
}

func (*discoveryErrorStatement) ColumnRawBlob(int) []byte {
	return nil
}

func (*discoveryErrorStatement) ColumnRawText(int) []byte {
	return nil
}

func (*discoveryErrorStatement) ColumnText(int) string {
	return ""
}

func (*discoveryErrorStatement) ColumnType(int) sqlite3.Datatype {
	return sqlite3.INTEGER
}

func (s *discoveryErrorStatement) Err() error {
	return s.stepErr
}

func (s *discoveryErrorStatement) Step() bool {
	if s.stepWait && s.interruptDone != nil {
		<-s.interruptDone
		s.stepErr = sqlite3.INTERRUPT
	}
	return false
}
