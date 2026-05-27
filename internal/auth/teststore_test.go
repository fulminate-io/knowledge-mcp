// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"sync"
)

// testStore is an in-test [Store] fake. Production uses keychain-backed
// [NewStore] which is unavailable in CI / non-interactive runners. The fake
// is intentionally minimal — Snapshot or other observability helpers do not
// belong here; tests assert on the Store interface alone.
type testStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newTestStore() *testStore {
	return &testStore{data: make(map[string]string)}
}

func (s *testStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (s *testStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *testStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; !ok {
		return ErrNotFound
	}
	delete(s.data, key)
	return nil
}
