package mcptoken

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNew_StartupState(t *testing.T) {
	createdAt := time.Date(2026, time.August, 14, 10, 0, 0, 0, time.UTC)
	rotatedAt := createdAt.Add(time.Hour)
	verifier := sha256.Sum256([]byte("dp-existing"))
	loadErr := errors.New("load failed")

	tests := []struct {
		name       string
		store      *fakeStore
		wantStatus Status
	}{
		{
			name:  "none when no persisted token exists",
			store: newFakeStore(),
			wantStatus: Status{
				State: StateNone,
			},
		},
		{
			name: "active from persisted verifier and metadata",
			store: &fakeStore{
				exists: true,
				state: PersistedState{
					Verifier:  verifier,
					CreatedAt: createdAt,
					RotatedAt: &rotatedAt,
				},
			},
			wantStatus: Status{
				State: StateActive,
				Metadata: Metadata{
					CreatedAt: createdAt,
					RotatedAt: &rotatedAt,
				},
			},
		},
		{
			name: "degraded when persisted state cannot be loaded",
			store: &fakeStore{
				loadErr: loadErr,
			},
			wantStatus: Status{
				State: StateDegraded,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, err := New(tt.store, func() time.Time { return createdAt })
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			assertStatus(t, service.Status(), tt.wantStatus)
		})
	}
}

func TestService_Create(t *testing.T) {
	createdAt := time.Date(2026, time.August, 14, 10, 0, 0, 0, time.UTC)
	store := newFakeStore()
	service, err := New(store, func() time.Time { return createdAt })
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	token, metadata, err := service.Create(context.Background())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	assertTokenStructure(t, token)
	assertMetadata(t, metadata, Metadata{CreatedAt: createdAt})
	assertStatus(t, service.Status(), Status{
		State:    StateActive,
		Metadata: metadata,
	})

	persisted, exists := store.persisted()
	if !exists {
		t.Fatal("persisted token does not exist")
	}
	if persisted.Verifier != sha256.Sum256([]byte(token)) {
		t.Fatal("persisted verifier does not match created token")
	}
	if strings.Contains(fmt.Sprintf("%#v %#v", service, store), token) {
		t.Fatal("plaintext token stored in runtime or persistent state")
	}
}

func TestService_CreateRejectsSecondToken(t *testing.T) {
	service, store, _ := newServiceWithToken(t)

	token, metadata, err := service.Create(context.Background())
	if !errors.Is(err, ErrTokenExists) {
		t.Fatalf("Create() error = %v, want ErrTokenExists", err)
	}
	if token != "" {
		t.Fatalf("Create() token = %q, want empty", token)
	}
	assertMetadata(t, metadata, Metadata{})
	if got := store.saveCount(); got != 1 {
		t.Fatalf("Save() calls = %d, want 1", got)
	}
}

func TestService_Verify(t *testing.T) {
	service, _, token := newServiceWithToken(t)

	if err := service.Verify(token); err != nil {
		t.Fatalf("Verify(created token) error = %v", err)
	}
	if err := service.Verify("dp-not-the-created-token"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify(wrong token) error = %v, want ErrInvalidToken", err)
	}

	err := service.Verify("dp-not-the-created-token")
	if strings.Contains(err.Error(), token) {
		t.Fatal("verification error contains plaintext token")
	}
}

func TestService_RotatePreservesCreationTime(t *testing.T) {
	createdAt := time.Date(2026, time.August, 14, 10, 0, 0, 0, time.UTC)
	rotatedAt := createdAt.Add(time.Hour)
	store := newFakeStore()
	now := createdAt
	service, err := New(store, func() time.Time { return now })
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	originalToken, originalMetadata, err := service.Create(context.Background())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	now = rotatedAt
	rotatedToken, metadata, err := service.Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}

	assertTokenStructure(t, rotatedToken)
	if rotatedToken == originalToken {
		t.Fatal("Rotate() returned the original token")
	}
	assertMetadata(t, metadata, Metadata{
		CreatedAt: originalMetadata.CreatedAt,
		RotatedAt: &rotatedAt,
	})
	if err := service.Verify(originalToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify(original token) error = %v, want ErrInvalidToken", err)
	}
	if err := service.Verify(rotatedToken); err != nil {
		t.Fatalf("Verify(rotated token) error = %v", err)
	}
}

func TestService_RevokeIsIdempotent(t *testing.T) {
	service, store, token := newServiceWithToken(t)

	if err := service.Revoke(context.Background()); err != nil {
		t.Fatalf("first Revoke() error = %v", err)
	}
	assertStatus(t, service.Status(), Status{State: StateNone})
	if err := service.Verify(token); !errors.Is(err, ErrNoToken) {
		t.Fatalf("Verify(revoked token) error = %v, want ErrNoToken", err)
	}
	if err := service.Revoke(context.Background()); err != nil {
		t.Fatalf("second Revoke() error = %v", err)
	}
	if got := store.deleteCount(); got != 1 {
		t.Fatalf("Delete() calls = %d, want 1", got)
	}
}

func TestService_RevokeThenCreateStartsFreshLifecycle(t *testing.T) {
	createdAt := time.Date(2026, time.August, 14, 10, 0, 0, 0, time.UTC)
	newCreatedAt := createdAt.Add(time.Hour)
	store := newFakeStore()
	now := createdAt
	service, err := New(store, func() time.Time { return now })
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	firstToken, firstMetadata, err := service.Create(context.Background())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := service.Revoke(context.Background()); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	now = newCreatedAt
	secondToken, secondMetadata, err := service.Create(context.Background())
	if err != nil {
		t.Fatalf("Create() after Revoke() error = %v", err)
	}

	if firstToken == secondToken {
		t.Fatal("Create() after Revoke() returned the original token")
	}
	if firstMetadata.CreatedAt == secondMetadata.CreatedAt {
		t.Fatal("Create() after Revoke() retained the original creation time")
	}
	assertMetadata(t, secondMetadata, Metadata{CreatedAt: newCreatedAt})
}

func TestService_PersistenceFailurePreservesRuntimeState(t *testing.T) {
	persistenceErr := errors.New("persistence failed")

	tests := []struct {
		name       string
		setup      func(t *testing.T) (*Service, *fakeStore, string)
		operation  func(*Service) error
		wantStatus Status
		verifyOld  bool
	}{
		{
			name: "create leaves no token when save fails",
			setup: func(t *testing.T) (*Service, *fakeStore, string) {
				t.Helper()
				store := newFakeStore()
				store.setSaveError(persistenceErr)
				service, err := New(store, time.Now)
				if err != nil {
					t.Fatalf("New() error = %v", err)
				}
				return service, store, ""
			},
			operation: func(service *Service) error {
				_, _, err := service.Create(context.Background())
				return err
			},
			wantStatus: Status{State: StateNone},
		},
		{
			name: "rotate retains active token when save fails",
			setup: func(t *testing.T) (*Service, *fakeStore, string) {
				t.Helper()
				service, store, token := newServiceWithToken(t)
				store.setSaveError(persistenceErr)
				return service, store, token
			},
			operation: func(service *Service) error {
				_, _, err := service.Rotate(context.Background())
				return err
			},
			wantStatus: Status{
				State: StateActive,
			},
			verifyOld: true,
		},
		{
			name: "revoke retains active token when delete fails",
			setup: func(t *testing.T) (*Service, *fakeStore, string) {
				t.Helper()
				service, store, token := newServiceWithToken(t)
				store.setDeleteError(persistenceErr)
				return service, store, token
			},
			operation: func(service *Service) error {
				return service.Revoke(context.Background())
			},
			wantStatus: Status{
				State: StateActive,
			},
			verifyOld: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, _, token := tt.setup(t)
			before := service.Status()

			err := tt.operation(service)
			if !errors.Is(err, persistenceErr) {
				t.Fatalf("operation error = %v, want persistence error", err)
			}

			after := service.Status()
			if tt.wantStatus.State == StateActive {
				tt.wantStatus.Metadata = before.Metadata
			}
			assertStatus(t, after, tt.wantStatus)
			if tt.verifyOld {
				if err := service.Verify(token); err != nil {
					t.Fatalf("Verify(original token) error = %v", err)
				}
			}
		})
	}
}

func TestService_DegradedRejectsRuntimeOperations(t *testing.T) {
	store := &fakeStore{loadErr: errors.New("load failed")}
	service, err := New(store, time.Now)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	tests := []struct {
		name      string
		operation func() error
	}{
		{
			name: "create",
			operation: func() error {
				_, _, err := service.Create(context.Background())
				return err
			},
		},
		{
			name: "rotate",
			operation: func() error {
				_, _, err := service.Rotate(context.Background())
				return err
			},
		},
		{
			name: "revoke",
			operation: func() error {
				return service.Revoke(context.Background())
			},
		},
		{
			name: "verify",
			operation: func() error {
				return service.Verify("dp-any-token")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.operation(); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("operation error = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestService_VerifyDoesNotBlockOnPersistence(t *testing.T) {
	service, store, token := newServiceWithToken(t)
	store.blockSaves()

	rotateDone := make(chan error, 1)
	go func() {
		_, _, err := service.Rotate(context.Background())
		rotateDone <- err
	}()
	<-store.saveStarted

	verifyDone := make(chan error, 1)
	go func() {
		verifyDone <- service.Verify(token)
	}()

	select {
	case err := <-verifyDone:
		if err != nil {
			t.Fatalf("Verify(original token) while Save() blocks error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Verify() blocked on persistence")
	}

	close(store.releaseSave)
	if err := <-rotateDone; err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
}

func TestService_ConcurrentVerifyAndLifecycleMutations(t *testing.T) {
	service, store, originalToken := newServiceWithToken(t)

	const verifierWorkers = 16
	const verificationsPerWorker = 200
	errs := make(chan error, verifierWorkers)
	var verifyGroup sync.WaitGroup
	for range verifierWorkers {
		verifyGroup.Add(1)
		go func() {
			defer verifyGroup.Done()
			for range verificationsPerWorker {
				err := service.Verify(originalToken)
				if err == nil || errors.Is(err, ErrInvalidToken) || errors.Is(err, ErrNoToken) {
					continue
				}
				errs <- fmt.Errorf("Verify() error = %w", err)
				return
			}
		}()
	}

	_, _, err := service.Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if err := service.Revoke(context.Background()); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	finalToken, _, err := service.Create(context.Background())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	verifyGroup.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	if got := service.Status().State; got != StateActive {
		t.Fatalf("Status().State = %q, want %q", got, StateActive)
	}
	if err := service.Verify(finalToken); err != nil {
		t.Fatalf("Verify(final token) error = %v", err)
	}
	persisted, exists := store.persisted()
	if !exists {
		t.Fatal("final persisted token does not exist")
	}
	if persisted.Verifier != sha256.Sum256([]byte(finalToken)) {
		t.Fatal("final persisted verifier does not match final token")
	}
}

func newServiceWithToken(t *testing.T) (*Service, *fakeStore, string) {
	t.Helper()
	store := newFakeStore()
	service, err := New(store, time.Now)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	token, _, err := service.Create(context.Background())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return service, store, token
}

func assertTokenStructure(t *testing.T, token string) {
	t.Helper()
	if !strings.HasPrefix(token, "dp-") {
		t.Fatalf("token = %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "dp-"))
	if err != nil || len(payload) != 32 {
		t.Fatal("invalid token payload")
	}
}

func assertStatus(t *testing.T, got, want Status) {
	t.Helper()
	if got.State != want.State {
		t.Fatalf("Status().State = %q, want %q", got.State, want.State)
	}
	assertMetadata(t, got.Metadata, want.Metadata)
}

func assertMetadata(t *testing.T, got, want Metadata) {
	t.Helper()
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("Metadata.CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if got.RotatedAt == nil && want.RotatedAt == nil {
		return
	}
	if got.RotatedAt == nil || want.RotatedAt == nil {
		t.Fatalf("Metadata.RotatedAt = %v, want %v", got.RotatedAt, want.RotatedAt)
	}
	if !got.RotatedAt.Equal(*want.RotatedAt) {
		t.Fatalf("Metadata.RotatedAt = %v, want %v", got.RotatedAt, want.RotatedAt)
	}
}

type fakeStore struct {
	mu sync.Mutex

	state  PersistedState
	exists bool

	loadErr   error
	saveErr   error
	deleteErr error

	saves   int
	deletes int

	saveStarted chan struct{}
	releaseSave chan struct{}
}

func newFakeStore() *fakeStore {
	return &fakeStore{}
}

func (s *fakeStore) Load(context.Context) (PersistedState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clonePersistedState(s.state), s.exists, s.loadErr
}

func (s *fakeStore) Save(_ context.Context, state PersistedState) error {
	s.mu.Lock()
	s.saves++
	saveErr := s.saveErr
	saveStarted := s.saveStarted
	releaseSave := s.releaseSave
	s.mu.Unlock()

	if saveStarted != nil {
		saveStarted <- struct{}{}
	}
	if releaseSave != nil {
		<-releaseSave
	}
	if saveErr != nil {
		return saveErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = clonePersistedState(state)
	s.exists = true
	return nil
}

func (s *fakeStore) Delete(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.state = PersistedState{}
	s.exists = false
	return nil
}

func (s *fakeStore) persisted() (PersistedState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clonePersistedState(s.state), s.exists
}

func (s *fakeStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func (s *fakeStore) deleteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deletes
}

func (s *fakeStore) setSaveError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveErr = err
}

func (s *fakeStore) setDeleteError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteErr = err
}

func (s *fakeStore) blockSaves() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveStarted = make(chan struct{}, 1)
	s.releaseSave = make(chan struct{})
}

func clonePersistedState(state PersistedState) PersistedState {
	return PersistedState{
		Verifier:  state.Verifier,
		CreatedAt: state.CreatedAt,
		RotatedAt: clonePersistedTime(state.RotatedAt),
	}
}

func clonePersistedTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
