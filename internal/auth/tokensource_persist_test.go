// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// capturingSlog is a [slog.Handler] that retains every emitted record so
// tests can assert on level + message + attributes. A formatted-output
// buffer cannot tell which record an attribute landed on, so the
// structured records are the assertion surface.
type capturingSlog struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingSlog) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingSlog) Handle(_ context.Context, rec slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, rec.Clone())
	return nil
}

func (h *capturingSlog) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *capturingSlog) WithGroup(string) slog.Handler { return h }

// find returns the first captured record at level whose message contains
// substr.
func (h *capturingSlog) find(level slog.Level, substr string) (slog.Record, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		if rec.Level == level && strings.Contains(rec.Message, substr) {
			return rec, true
		}
	}
	return slog.Record{}, false
}

// installCapturingSlog swaps the default logger for a capturing handler
// and restores the previous one when the test ends.
func installCapturingSlog(t *testing.T) *capturingSlog {
	t.Helper()
	h := &capturingSlog{}
	prior := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return h
}

// intAttr reads a named integer attribute off a record, failing the test
// when it is absent.
func intAttr(t *testing.T, rec slog.Record, key string) int64 {
	t.Helper()
	var (
		out   int64
		found bool
	)
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key != key {
			return true
		}
		out, found = a.Value.Int64(), true
		return false
	})
	if !found {
		t.Fatalf("record %q carries no %q attribute", rec.Message, key)
	}
	return out
}

// countingStore lets every write land normally while counting the writes
// of the refresh token. It is the known-positive control for the retry
// assertions: a healthy store persists in exactly one attempt.
type countingStore struct {
	*testStore
	sets atomic.Int32
}

func (s *countingStore) Set(ctx context.Context, key, value string) error {
	if key == KeyRefreshToken {
		s.sets.Add(1)
	}
	return s.testStore.Set(ctx, key, value)
}

// silentDropStore reports a successful Set of the refresh token but never
// lets the value land — the shape of a credential store whose write is
// accepted and then lost. Reads still resolve the previously stored
// value, so a read-back observes the OLD token.
type silentDropStore struct {
	*testStore
	sets atomic.Int32
}

func (s *silentDropStore) Set(ctx context.Context, key, value string) error {
	if key == KeyRefreshToken {
		s.sets.Add(1)
		return nil
	}
	return s.testStore.Set(ctx, key, value)
}

// rotatingTokenServer serves a successful refresh that rotates the
// refresh token to newRefresh, returning the server and the access token
// it mints.
func rotatingTokenServer(t *testing.T, newRefresh string) (*httptest.Server, string) {
	t.Helper()
	access := signTestJWT(t, []string{PermMCPKnowledgeRead}, time.Now().Add(time.Hour).Unix())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenResponse{ //nolint:gosec // test fixture with literal string values
			AccessToken:  access,
			RefreshToken: newRefresh,
			TokenType:    "Bearer",
			ExpiresIn:    3600,
		})
	}))
	t.Cleanup(srv.Close)
	return srv, access
}

// seedRefreshSource builds a token source over base with the given
// refresh token already stored.
func seedRefreshSource(t *testing.T, base *testStore, tokenEndpoint, refresh string) *OAuthTokenSource {
	t.Helper()
	if err := base.Set(context.Background(), KeyRefreshToken, refresh); err != nil {
		t.Fatalf("seed refresh token: %v", err)
	}
	return newOAuthSourceForTest(base, tokenEndpoint)
}

// TestOAuthTokenSource_PersistSucceedsInOneAttempt is the known-positive
// control for the retry + read-back assertions below: against a healthy
// store the rotation persists on the first write, the new value is what a
// sibling process would read, and nothing is logged at ERROR. Without it
// a persist counter that never increments and a store that never gets
// written would be indistinguishable from a clean run.
func TestOAuthTokenSource_PersistSucceedsInOneAttempt(t *testing.T) {
	logs := installCapturingSlog(t)
	srv, access := rotatingTokenServer(t, "frt_new")

	base := newTestStore()
	src := seedRefreshSource(t, base, srv.URL, "frt_old")
	store := &countingStore{testStore: base}
	src.store = store

	tok, _, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != access {
		t.Errorf("expected the newly acquired access token")
	}
	if got := store.sets.Load(); got != 1 {
		t.Errorf("a healthy persist must take exactly 1 write, got %d", got)
	}
	stored, err := base.Get(context.Background(), KeyRefreshToken)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if stored != "frt_new" {
		t.Errorf("expected the rotated token in the store, got %q", stored)
	}
	if rec, ok := logs.find(slog.LevelError, "persist"); ok {
		t.Errorf("a healthy persist must not log at ERROR, got %q", rec.Message)
	}
}

// TestOAuthTokenSource_PersistReadBackMismatchRetriesThenLogsError pins
// the read-back verification: a Set that returns nil without the value
// surviving is a FAILED persist, because a sibling process reading the
// store would still find the consumed token. The write is retried, and
// when it never lands the failure is reported at ERROR — the acquisition
// itself still succeeds.
func TestOAuthTokenSource_PersistReadBackMismatchRetriesThenLogsError(t *testing.T) {
	logs := installCapturingSlog(t)
	srv, access := rotatingTokenServer(t, "frt_new")

	base := newTestStore()
	src := seedRefreshSource(t, base, srv.URL, "frt_old")
	store := &silentDropStore{testStore: base}
	src.store = store

	tok, _, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("a failed persist must not surface as an error, got %v", err)
	}
	if tok != access {
		t.Errorf("expected the newly acquired access token despite the failed persist")
	}

	attempts := store.sets.Load()
	if attempts < 2 {
		t.Errorf("a Set that reports success but does not survive read-back must be retried, got %d attempt(s)", attempts)
	}

	rec, ok := logs.find(slog.LevelError, "could not persist the rotated refresh token")
	if !ok {
		t.Fatal("expected an ERROR record naming the failed rotation persist")
	}
	if got := intAttr(t, rec, "attempts"); got != int64(attempts) {
		t.Errorf("logged attempts=%d but the store saw %d write(s)", got, attempts)
	}

	stored, err := base.Get(context.Background(), KeyRefreshToken)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if stored != "frt_old" {
		t.Errorf("expected the store to still hold frt_old, got %q", stored)
	}
}

// TestOAuthTokenSource_StaleStoredTokenBreadcrumb covers the victim side:
// the process whose stored refresh token was consumed by a sibling that
// then failed to persist the replacement. Its refresh is rejected, and
// the breadcrumb names the likely cause without altering the error the
// caller receives. The server_error case is the control — a refresh
// failure unrelated to the credential must NOT claim the token was
// consumed.
func TestOAuthTokenSource_StaleStoredTokenBreadcrumb(t *testing.T) {
	cases := []struct {
		name           string
		status         int
		body           string
		wantBreadcrumb bool
	}{
		{"401 unauthorized", http.StatusUnauthorized, `{"error":"unauthorized","error_description":"token already used"}`, true},
		{"400 invalid_grant", http.StatusBadRequest, `{"error":"invalid_grant"}`, true},
		{"500 server_error", http.StatusInternalServerError, `{"error":"server_error"}`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := installCapturingSlog(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			src := seedRefreshSource(t, newTestStore(), srv.URL, "frt_consumed")

			_, _, err := src.Token(context.Background())
			if err == nil {
				t.Fatal("expected the refresh to fail")
			}
			switch tc.status {
			case http.StatusBadRequest:
				if !errors.Is(err, ErrInvalidGrant) {
					t.Errorf("expected ErrInvalidGrant, got %v", err)
				}
			case http.StatusUnauthorized:
				if errors.Is(err, ErrInvalidGrant) {
					t.Error("a plain 401 must not be reclassified as invalid_grant")
				}
				if !strings.Contains(err.Error(), "unauthorized") {
					t.Errorf("the returned error must be unchanged, got %v", err)
				}
			}

			if _, ok := logs.find(slog.LevelWarn, "already consumed"); ok != tc.wantBreadcrumb {
				t.Errorf("stale-token breadcrumb present=%v, want %v (error: %v)", ok, tc.wantBreadcrumb, err)
			}
		})
	}
}
