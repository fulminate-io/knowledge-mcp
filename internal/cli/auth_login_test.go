// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// fakeAuthStore is the cli-package test fake for [auth.Store]. Production
// uses the keychain-backed [auth.NewStore], unavailable in CI.
type fakeAuthStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newFakeAuthStore() *fakeAuthStore {
	return &fakeAuthStore{data: make(map[string]string)}
}

func (s *fakeAuthStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return "", auth.ErrNotFound
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
		return auth.ErrNotFound
	}
	delete(s.data, key)
	return nil
}

// withMemoryStore swaps newStoreFn for a fresh in-memory fake store for
// the duration of a test. The returned *fakeAuthStore lets the test
// assert what the subcommand persisted.
func withMemoryStore(t *testing.T) *fakeAuthStore {
	t.Helper()
	mem := newFakeAuthStore()
	orig := newStoreFn
	newStoreFn = func() (auth.Store, error) { return mem, nil }
	t.Cleanup(func() { newStoreFn = orig })
	return mem
}
