// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// writeRecorderStore records the key of EVERY mutating call, whatever the
// key, so a test asserting "this code path never writes" has something that
// would have noticed if it did. Reads delegate to the embedded fake.
//
// The catch-all matters: a recorder that only watched the keys a test expects
// would report zero for a write to some other key, which is the failure the
// assertion exists to catch.
type writeRecorderStore struct {
	*testStore
	mu     sync.Mutex
	writes []string
}

func (s *writeRecorderStore) Set(ctx context.Context, key, value string) error {
	s.mu.Lock()
	s.writes = append(s.writes, "set:"+key)
	s.mu.Unlock()
	return s.testStore.Set(ctx, key, value)
}

func (s *writeRecorderStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	s.writes = append(s.writes, "delete:"+key)
	s.mu.Unlock()
	return s.testStore.Delete(ctx, key)
}

func (s *writeRecorderStore) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

// seedSession writes a session token pair directly into the backing fake,
// standing in for whatever process owns the session.
func seedSession(t *testing.T, store Store, token string, expiry time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := store.Set(ctx, KeyAccessToken, token); err != nil {
		t.Fatalf("seed access token: %v", err)
	}
	if err := store.Set(ctx, KeyAccessTokenExpiry, expiry.UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed access token expiry: %v", err)
	}
}

// TestReadOnlyTokenSource_ServesPublishedSessionWithoutWriting is the
// load-bearing guard for the read-only contract: the source serves a valid
// published session and reaches the store for reads ONLY.
func TestReadOnlyTokenSource_ServesPublishedSessionWithoutWriting(t *testing.T) {
	base := newTestStore()
	access := signTestJWT(t, []string{PermMCPKnowledgeRead, PermDeployBYOC}, time.Now().Add(time.Hour).Unix())
	seedSession(t, base, access, time.Now().Add(time.Hour))

	rec := &writeRecorderStore{testStore: base}
	src := NewReadOnlyTokenSource(rec)

	tok, perms, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != access {
		t.Errorf("returned token is not the published one")
	}
	if !perms.Has(PermDeployBYOC) {
		t.Errorf("permissions not carried through from the token claims: %v", perms.List())
	}
	if got := rec.recorded(); len(got) != 0 {
		t.Errorf("the read-only source wrote to the store: %v", got)
	}

	// Known-positive control: the recorder above reads zero only because
	// nothing wrote. Drive it non-zero to prove it would have noticed.
	if err := rec.Set(context.Background(), KeyAccessToken, access); err != nil {
		t.Fatalf("control Set: %v", err)
	}
	if got := rec.recorded(); len(got) != 1 || got[0] != "set:"+KeyAccessToken {
		t.Fatalf("write recorder did not register a real write: %v", got)
	}
}

// TestReadOnlyTokenSource_RefusesUnusableSessions covers every way a session
// can fail to be usable. Each arm must also stay write-free: a source that
// "helpfully" cleared or repaired a bad session would be writing.
func TestReadOnlyTokenSource_RefusesUnusableSessions(t *testing.T) {
	cases := []struct {
		name  string
		seed  func(t *testing.T, store Store)
		want  error
		token string
	}{
		{
			name: "no session stored",
			seed: func(*testing.T, Store) {},
			want: ErrNoSession,
		},
		{
			name: "expired session",
			seed: func(t *testing.T, store Store) {
				seedSession(t, store, "tok", time.Now().Add(-time.Minute))
			},
			want: ErrSessionExpired,
		},
		{
			name: "unparseable expiry fails closed",
			seed: func(t *testing.T, store Store) {
				ctx := context.Background()
				if err := store.Set(ctx, KeyAccessToken, "tok"); err != nil {
					t.Fatalf("seed: %v", err)
				}
				if err := store.Set(ctx, KeyAccessTokenExpiry, "not-a-timestamp"); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
			want: ErrSessionExpired,
		},
		{
			name: "token present but expiry missing",
			seed: func(t *testing.T, store Store) {
				if err := store.Set(context.Background(), KeyAccessToken, "tok"); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
			want: ErrNoSession,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newTestStore()
			tc.seed(t, base)
			rec := &writeRecorderStore{testStore: base}

			tok, _, err := NewReadOnlyTokenSource(rec).Token(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("Token err = %v, want %v", err, tc.want)
			}
			if tok != "" {
				t.Errorf("a refused session still returned a token")
			}
			if got := rec.recorded(); len(got) != 0 {
				t.Errorf("the read-only source wrote while refusing: %v", got)
			}
		})
	}
}

// TestReadOnlyTokenSource_NotRefreshing pins the structural half of the
// contract. Implementing RefreshingTokenSource would invite the sync
// transport to call ForceRefresh on a 401 — a refresh this source has no
// credential for and could not persist if it did.
func TestReadOnlyTokenSource_NotRefreshing(t *testing.T) {
	var _ TokenSource = (*ReadOnlyTokenSource)(nil)
	if _, ok := any(NewReadOnlyTokenSource(newTestStore())).(RefreshingTokenSource); ok {
		t.Fatal("ReadOnlyTokenSource must NOT implement RefreshingTokenSource — " +
			"it holds no refresh credential and must never write, so a 401 has to surface to the caller")
	}
}

// TestOAuthTokenSource_PublishesSessionForReaders is the end-to-end handoff:
// the process that owns the session refreshes, and a reader holding nothing
// but the same store can then use that session without refreshing or writing
// anything itself. This is what lets a sibling process be authenticated
// without rotating the refresh token out from under everyone else.
func TestOAuthTokenSource_PublishesSessionForReaders(t *testing.T) {
	srv, access := rotatingTokenServer(t, "frt_new")
	base := newTestStore()
	owner := seedRefreshSource(t, base, srv.URL, "frt_old")

	if _, _, err := owner.Token(context.Background()); err != nil {
		t.Fatalf("owner Token: %v", err)
	}

	rec := &writeRecorderStore{testStore: base}
	tok, perms, err := NewReadOnlyTokenSource(rec).Token(context.Background())
	if err != nil {
		t.Fatalf("reader Token after the owner refreshed: %v", err)
	}
	if tok != access {
		t.Errorf("reader served a different token than the owner published")
	}
	if !perms.Has(PermMCPKnowledgeRead) {
		t.Errorf("reader lost the published permissions: %v", perms.List())
	}
	if got := rec.recorded(); len(got) != 0 {
		t.Errorf("the reader wrote to the store: %v", got)
	}
}
