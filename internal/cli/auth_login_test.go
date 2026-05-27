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

// TestParsePermissions covers the comma-split + trim logic and the
// default-set fallback when --permissions is empty.
func TestParsePermissions(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty defaults", "", []string{auth.PermMCPKnowledgeRead, auth.PermMCPKnowledgeWrite}},
		{"single", "mcp:knowledge:read", []string{"mcp:knowledge:read"}},
		{"comma-pair", "mcp:knowledge:read,mcp:knowledge:write", []string{"mcp:knowledge:read", "mcp:knowledge:write"}},
		{"whitespace", " mcp:knowledge:read , mcp:knowledge:write ", []string{"mcp:knowledge:read", "mcp:knowledge:write"}},
		{"empty entries dropped", ",,mcp:knowledge:read,,", []string{"mcp:knowledge:read"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePermissions(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parsePermissions(%q): got %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parsePermissions(%q)[%d]: got %q, want %q",
						tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}
