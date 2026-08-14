package mcptoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrTokenExists  = errors.New("mcp access token already exists")
	ErrNoToken      = errors.New("mcp access token does not exist")
	ErrInvalidToken = errors.New("invalid mcp access token")
	ErrUnavailable  = errors.New("mcp token service unavailable")
)

type State string

const (
	StateNone     State = "none"
	StateActive   State = "active"
	StateDegraded State = "degraded"
)

type Metadata struct {
	CreatedAt time.Time
	RotatedAt *time.Time
}

type PersistedState struct {
	Verifier  [sha256.Size]byte
	CreatedAt time.Time
	RotatedAt *time.Time
}

type Store interface {
	Load(context.Context) (PersistedState, bool, error)
	Save(context.Context, PersistedState) error
	Delete(context.Context) error
}

type Status struct {
	State    State
	Metadata Metadata
}

type Service struct {
	store Store
	now   func() time.Time

	mutationMu sync.Mutex
	runtimeMu  sync.RWMutex
	state      State
	verifier   [sha256.Size]byte
	metadata   Metadata
}

func New(store Store, now func() time.Time) (*Service, error) {
	if store == nil {
		return nil, errors.New("mcp token store is nil")
	}
	if now == nil {
		now = time.Now
	}

	service := &Service{
		store: store,
		now:   now,
		state: StateNone,
	}

	persisted, exists, err := store.Load(context.Background())
	if err != nil {
		service.state = StateDegraded
		return service, nil
	}
	if !exists {
		return service, nil
	}

	service.state = StateActive
	service.verifier = persisted.Verifier
	service.metadata = metadataFromPersisted(persisted)
	return service, nil
}

func (s *Service) Create(ctx context.Context) (string, Metadata, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	switch s.currentState() {
	case StateDegraded:
		return "", Metadata{}, ErrUnavailable
	case StateActive:
		return "", Metadata{}, ErrTokenExists
	}

	token, verifier, err := generateToken()
	if err != nil {
		return "", Metadata{}, err
	}
	metadata := Metadata{CreatedAt: s.now()}
	persisted := PersistedState{
		Verifier:  verifier,
		CreatedAt: metadata.CreatedAt,
	}
	if err := s.store.Save(ctx, persisted); err != nil {
		return "", Metadata{}, fmt.Errorf("saving mcp access token: %w", err)
	}

	s.commitActive(verifier, metadata)
	return token, cloneMetadata(metadata), nil
}

func (s *Service) Status() Status {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return Status{
		State:    s.state,
		Metadata: cloneMetadata(s.metadata),
	}
}

func (s *Service) Rotate(ctx context.Context) (string, Metadata, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	current := s.Status()
	switch current.State {
	case StateDegraded:
		return "", Metadata{}, ErrUnavailable
	case StateNone:
		return "", Metadata{}, ErrNoToken
	}

	token, verifier, err := generateToken()
	if err != nil {
		return "", Metadata{}, err
	}
	rotatedAt := s.now()
	metadata := Metadata{
		CreatedAt: current.Metadata.CreatedAt,
		RotatedAt: &rotatedAt,
	}
	persisted := PersistedState{
		Verifier:  verifier,
		CreatedAt: metadata.CreatedAt,
		RotatedAt: cloneTime(metadata.RotatedAt),
	}
	if err := s.store.Save(ctx, persisted); err != nil {
		return "", Metadata{}, fmt.Errorf("saving rotated mcp access token: %w", err)
	}

	s.commitActive(verifier, metadata)
	return token, cloneMetadata(metadata), nil
}

func (s *Service) Revoke(ctx context.Context) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	switch s.currentState() {
	case StateDegraded:
		return ErrUnavailable
	case StateNone:
		return nil
	}

	if err := s.store.Delete(ctx); err != nil {
		return fmt.Errorf("deleting mcp access token: %w", err)
	}

	s.runtimeMu.Lock()
	s.state = StateNone
	s.verifier = [sha256.Size]byte{}
	s.metadata = Metadata{}
	s.runtimeMu.Unlock()
	return nil
}

func (s *Service) Verify(token string) error {
	s.runtimeMu.RLock()
	state := s.state
	expected := s.verifier
	s.runtimeMu.RUnlock()

	switch state {
	case StateDegraded:
		return ErrUnavailable
	case StateNone:
		return ErrNoToken
	}

	candidate := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(candidate[:], expected[:]) != 1 {
		return ErrInvalidToken
	}
	return nil
}

func (s *Service) currentState() State {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.state
}

func (s *Service) commitActive(verifier [sha256.Size]byte, metadata Metadata) {
	s.runtimeMu.Lock()
	s.state = StateActive
	s.verifier = verifier
	s.metadata = cloneMetadata(metadata)
	s.runtimeMu.Unlock()
}

func generateToken() (string, [sha256.Size]byte, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", [sha256.Size]byte{}, fmt.Errorf("generating mcp token: %w", err)
	}
	token := "dp-" + base64.RawURLEncoding.EncodeToString(random)
	return token, sha256.Sum256([]byte(token)), nil
}

func metadataFromPersisted(state PersistedState) Metadata {
	return Metadata{
		CreatedAt: state.CreatedAt,
		RotatedAt: cloneTime(state.RotatedAt),
	}
}

func cloneMetadata(metadata Metadata) Metadata {
	return Metadata{
		CreatedAt: metadata.CreatedAt,
		RotatedAt: cloneTime(metadata.RotatedAt),
	}
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
