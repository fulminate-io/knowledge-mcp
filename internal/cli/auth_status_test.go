// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// writeRecorder wraps the cli store fake and records every mutating call,
// whatever the key, so a test asserting "asking the question changes nothing"
// has something that would have noticed a write. Reads delegate.
type writeRecorder struct {
	*fakeAuthStore
	mu     sync.Mutex
	writes []string
}

func (s *writeRecorder) Set(ctx context.Context, key, value string) error {
	s.mu.Lock()
	s.writes = append(s.writes, "set:"+key)
	s.mu.Unlock()
	return s.fakeAuthStore.Set(ctx, key, value)
}

func (s *writeRecorder) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	s.writes = append(s.writes, "delete:"+key)
	s.mu.Unlock()
	return s.fakeAuthStore.Delete(ctx, key)
}

func (s *writeRecorder) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

// withRecordingStore installs a recording in-memory store as the cli store
// constructor for the duration of a test.
func withRecordingStore(t *testing.T) *writeRecorder {
	t.Helper()
	rec := &writeRecorder{fakeAuthStore: newFakeAuthStore()}
	orig := newStoreFn
	newStoreFn = func() (auth.Store, error) { return rec, nil }
	t.Cleanup(func() { newStoreFn = orig })
	return rec
}

// TestAuthStatusCmd_Outcomes pins the exit-class of every outcome. The
// distinction that matters is between "definitively no session" (which wraps
// ErrNoValidSession and becomes exit 2) and "could not determine" (which does
// not, and stays exit 1) — a caller that confuses them turns a version skew or
// an unreadable keychain into a confident refusal.
func TestAuthStatusCmd_Outcomes(t *testing.T) {
	valid := func(t *testing.T, rec *writeRecorder) {
		t.Helper()
		ctx := context.Background()
		// Seed through the embedded fake so the recorder stays clean for the
		// zero-write assertion.
		if err := rec.fakeAuthStore.Set(ctx, auth.KeyAccessToken, "at_live"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := rec.fakeAuthStore.Set(ctx, auth.KeyAccessTokenExpiry,
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	t.Run("valid session exits zero", func(t *testing.T) {
		rec := withRecordingStore(t)
		valid(t, rec)

		if err := AuthStatusCmd(nil); err != nil {
			t.Fatalf("AuthStatusCmd with a valid session = %v, want nil", err)
		}
		if got := rec.recorded(); len(got) != 0 {
			t.Errorf("asking the question wrote to the store: %v", got)
		}
	})

	t.Run("no session is definitive", func(t *testing.T) {
		rec := withRecordingStore(t)

		err := AuthStatusCmd(nil)
		if !errors.Is(err, ErrNoValidSession) {
			t.Fatalf("AuthStatusCmd with no session = %v, want ErrNoValidSession", err)
		}
		if got := rec.recorded(); len(got) != 0 {
			t.Errorf("the no-session path wrote to the store: %v", got)
		}
	})

	t.Run("expired session is definitive", func(t *testing.T) {
		rec := withRecordingStore(t)
		ctx := context.Background()
		if err := rec.fakeAuthStore.Set(ctx, auth.KeyAccessToken, "at_stale"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := rec.fakeAuthStore.Set(ctx, auth.KeyAccessTokenExpiry,
			time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)); err != nil {
			t.Fatalf("seed: %v", err)
		}

		err := AuthStatusCmd(nil)
		if !errors.Is(err, ErrNoValidSession) {
			t.Fatalf("AuthStatusCmd with an expired session = %v, want ErrNoValidSession", err)
		}
		if got := rec.recorded(); len(got) != 0 {
			t.Errorf("the expired path wrote to the store: %v", got)
		}
	})

	// A machine logged in before session publishing existed has a refresh
	// token and no published session. It has no usable session (exit 2), but
	// telling its operator they are "not logged in" would send them to
	// re-authenticate a login that works. Caught by a live probe, not by the
	// original tests.
	t.Run("logged in but unpublished does not claim logged out", func(t *testing.T) {
		rec := withRecordingStore(t)
		if err := rec.fakeAuthStore.Set(context.Background(), auth.KeyRefreshToken, "frt_live"); err != nil {
			t.Fatalf("seed: %v", err)
		}

		err := AuthStatusCmd(nil)
		if !errors.Is(err, ErrNoValidSession) {
			t.Fatalf("AuthStatusCmd = %v, want ErrNoValidSession (there is still no usable session)", err)
		}
		if !strings.Contains(err.Error(), "logged in") ||
			strings.Contains(err.Error(), "not logged in") {
			t.Errorf("reason = %q, want it to say the login exists and the session is merely unpublished", err)
		}
		if got := rec.recorded(); len(got) != 0 {
			t.Errorf("the unpublished path wrote to the store: %v", got)
		}
	})

	// The same distinction on the expiry arm.
	t.Run("logged in with an expired session names the refresh, not a re-login", func(t *testing.T) {
		rec := withRecordingStore(t)
		ctx := context.Background()
		for k, v := range map[string]string{
			auth.KeyRefreshToken:      "frt_live",
			auth.KeyAccessToken:       "at_stale",
			auth.KeyAccessTokenExpiry: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		} {
			if err := rec.fakeAuthStore.Set(ctx, k, v); err != nil {
				t.Fatalf("seed %s: %v", k, err)
			}
		}

		err := AuthStatusCmd(nil)
		if !errors.Is(err, ErrNoValidSession) {
			t.Fatalf("AuthStatusCmd = %v, want ErrNoValidSession", err)
		}
		if !strings.Contains(err.Error(), "refresh") {
			t.Errorf("reason = %q, want it to name the refresh that will fix this", err)
		}
	})

	// An unreadable store must NOT read as "no session": we failed to ask the
	// question, which is not the same as learning the answer is no.
	t.Run("unreadable store is indeterminate, not a refusal", func(t *testing.T) {
		orig := newStoreFn
		newStoreFn = func() (auth.Store, error) { return nil, errors.New("keychain denied") }
		t.Cleanup(func() { newStoreFn = orig })

		err := AuthStatusCmd(nil)
		if err == nil {
			t.Fatal("an unreadable store must be an error")
		}
		if errors.Is(err, ErrNoValidSession) {
			t.Error("an unreadable store was reported as definitively no session — " +
				"that turns a broken keychain into a confident logged-out verdict")
		}
	})

	// Known-positive control for the zero-write assertions above: the recorder
	// registers a real write, so a zero elsewhere means nothing wrote rather
	// than that the recorder never looked.
	t.Run("control: the write recorder registers a real write", func(t *testing.T) {
		rec := withRecordingStore(t)
		if err := rec.Set(context.Background(), auth.KeyAccessToken, "x"); err != nil {
			t.Fatalf("control Set: %v", err)
		}
		if got := rec.recorded(); len(got) != 1 || got[0] != "set:"+auth.KeyAccessToken {
			t.Fatalf("write recorder did not register a real write: %v", got)
		}
	})
}
