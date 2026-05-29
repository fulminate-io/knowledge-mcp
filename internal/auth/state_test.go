// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeAuthStore is an in-test Store with a Gets counter so tests can assert
// keychain pressure (or absence thereof). Mirrors the testStore shape in
// teststore_test.go but adds the per-call counter the AuthState TTL tests need.
type fakeAuthStore struct {
	mu     sync.Mutex
	data   map[string]string
	getErr error // when non-nil, every Get returns this error (overrides data)
	gets   int
}

func newFakeAuthStore() *fakeAuthStore {
	return &fakeAuthStore{data: make(map[string]string)}
}

func (s *fakeAuthStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if s.getErr != nil {
		return "", s.getErr
	}
	v, ok := s.data[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (s *fakeAuthStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *fakeAuthStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; !ok {
		return ErrNotFound
	}
	delete(s.data, key)
	return nil
}

func (s *fakeAuthStore) GetCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets
}

// TestAuthState_FreshCheckReadsKeychain — first IsLoggedIn hits the store
// (Gets=1) and returns true; second within TTL stays at Gets=1 (cached).
func TestAuthState_FreshCheckReadsKeychain(t *testing.T) {
	store := newFakeAuthStore()
	if err := store.Set(context.Background(), KeyRefreshToken, "tok-abc"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	as := NewAuthState(store, time.Hour) // long TTL — second call must hit cache

	if !as.IsLoggedIn(context.Background()) {
		t.Fatal("first IsLoggedIn: want true (token seeded), got false")
	}
	if got := store.GetCount(); got != 1 {
		t.Errorf("first call: Gets = %d, want 1", got)
	}

	if !as.IsLoggedIn(context.Background()) {
		t.Fatal("second IsLoggedIn within TTL: want true (cached), got false")
	}
	if got := store.GetCount(); got != 1 {
		t.Errorf("second call within TTL: Gets = %d, want 1 (cache hit)", got)
	}
}

// TestAuthState_TTLExpiryRechecks — with ttl=1ms, the second call after
// sleeping past TTL re-hits the store (Gets=2).
func TestAuthState_TTLExpiryRechecks(t *testing.T) {
	store := newFakeAuthStore()
	if err := store.Set(context.Background(), KeyRefreshToken, "tok"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	as := NewAuthState(store, time.Millisecond)

	if !as.IsLoggedIn(context.Background()) {
		t.Fatal("first IsLoggedIn: want true")
	}
	if got := store.GetCount(); got != 1 {
		t.Fatalf("first call: Gets = %d, want 1", got)
	}

	// Wait past the TTL — 50ms gives darwin/CI a comfortable margin.
	time.Sleep(50 * time.Millisecond)

	if !as.IsLoggedIn(context.Background()) {
		t.Fatal("second IsLoggedIn after TTL: want true")
	}
	if got := store.GetCount(); got != 2 {
		t.Errorf("after TTL: Gets = %d, want 2 (re-check)", got)
	}
}

// TestAuthState_LoginMidSession — seed store with NO token → first IsLoggedIn
// returns false; set token mid-session; after TTL, IsLoggedIn returns true.
// Verifies the mid-session login detection contract from the ticket.
func TestAuthState_LoginMidSession(t *testing.T) {
	store := newFakeAuthStore() // empty
	as := NewAuthState(store, time.Millisecond)

	if as.IsLoggedIn(context.Background()) {
		t.Fatal("first IsLoggedIn (no token): want false, got true")
	}

	// User runs `knowledge login` in another process — refresh token appears.
	if err := store.Set(context.Background(), KeyRefreshToken, "tok-fresh"); err != nil {
		t.Fatalf("login: %v", err)
	}

	// Wait past TTL so the cache expires.
	time.Sleep(50 * time.Millisecond)

	if !as.IsLoggedIn(context.Background()) {
		t.Fatal("after login + TTL: want true (cache flipped), got false")
	}
}

// TestAuthState_ErrNotFoundIsFalseNotError — Store.Get returning ErrNotFound
// causes IsLoggedIn to return false (not panicking, not propagating the sentinel).
func TestAuthState_ErrNotFoundIsFalseNotError(t *testing.T) {
	store := newFakeAuthStore() // empty -> Get returns ErrNotFound
	as := NewAuthState(store, time.Hour)

	// Must return false, not panic. Repeated call must also return false
	// (cached false is a valid state).
	if as.IsLoggedIn(context.Background()) {
		t.Fatal("ErrNotFound from store: want false, got true")
	}
	if as.IsLoggedIn(context.Background()) {
		t.Fatal("second call (cached false): want false, got true")
	}
}

// TestAuthState_BackendErrorHoldsLastKnown — a non-ErrNotFound backend
// failure does not flip the cached value; the prior state is preserved. Not
// a plan criterion but tightens the error-handling contract.
func TestAuthState_BackendErrorHoldsLastKnown(t *testing.T) {
	store := newFakeAuthStore()
	if err := store.Set(context.Background(), KeyRefreshToken, "tok"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	as := NewAuthState(store, time.Millisecond)

	if !as.IsLoggedIn(context.Background()) {
		t.Fatal("first IsLoggedIn: want true")
	}

	// Simulate a transient backend failure on the next probe.
	store.mu.Lock()
	store.getErr = errors.New("dbus: connection refused")
	store.mu.Unlock()

	time.Sleep(50 * time.Millisecond)

	if !as.IsLoggedIn(context.Background()) {
		t.Fatal("backend error: want last-known true, got false")
	}
}
