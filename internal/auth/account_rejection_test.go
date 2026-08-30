// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// forbiddenBody is the gateway's membership rejection.
const forbiddenBody = `{"error":"account_forbidden","error_description":"caller is not a member of the requested account"}`

// TestClassifyAccountRejection_DecisionTable pins the whole decision table,
// including the three descriptions that must NOT latch.
func TestClassifyAccountRejection_DecisionTable(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantLatch  bool
		wantReason []string // substrings the reason must carry ("" = reason must be empty)
	}{
		{
			name:       "403 account_forbidden latches",
			status:     http.StatusForbidden,
			body:       forbiddenBody,
			wantLatch:  true,
			wantReason: []string{"not a member of the selected Fulminate account", "knowledge accounts", "knowledge account use"},
		},
		{
			name:       "400 bad_request latches",
			status:     http.StatusBadRequest,
			body:       `{"error":"bad_request","error_description":"invalid account id"}`,
			wantLatch:  true,
			wantReason: []string{"stored Fulminate account id is malformed", "knowledge account use"},
		},
		{
			name:       "403 subscription_required / active paid subscription required latches",
			status:     http.StatusForbidden,
			body:       `{"error":"subscription_required","error_description":"active paid subscription required","upgrade_url":"https://example.test/upgrade"}`,
			wantLatch:  true,
			wantReason: []string{"no active subscription", "no cloud graph access", "https://example.test/upgrade", "knowledge account use"},
		},
		{
			name:       "403 subscription_required / no account associated with this user latches",
			status:     http.StatusForbidden,
			body:       `{"error":"subscription_required","error_description":"no account associated with this user"}`,
			wantLatch:  true,
			wantReason: []string{"no active subscription", "subscribe at billing"},
		},
		{
			name:       "403 subscription_required / account lookup failed does NOT latch",
			status:     http.StatusForbidden,
			body:       `{"error":"subscription_required","error_description":"account lookup failed"}`,
			wantLatch:  false,
			wantReason: []string{"could not complete the account check", "account lookup failed"},
		},
		{
			name:       "403 subscription_required / subscription lookup failed does NOT latch",
			status:     http.StatusForbidden,
			body:       `{"error":"subscription_required","error_description":"subscription lookup failed"}`,
			wantLatch:  false,
			wantReason: []string{"could not complete the account check", "subscription lookup failed"},
		},
		{
			name:       "403 subscription_required / missing authentication context does NOT latch",
			status:     http.StatusForbidden,
			body:       `{"error":"subscription_required","error_description":"missing authentication context"}`,
			wantLatch:  false,
			wantReason: []string{"could not complete the account check", "missing authentication context"},
		},
		{
			name:      "401 is never an account rejection",
			status:    http.StatusUnauthorized,
			body:      forbiddenBody,
			wantLatch: false,
		},
		{
			name:      "unparseable body is never a rejection",
			status:    http.StatusForbidden,
			body:      "<html>gateway timeout</html>",
			wantLatch: false,
		},
		{
			name:      "403 with an unknown slug is not a rejection",
			status:    http.StatusForbidden,
			body:      `{"error":"scope_missing","error_description":"sync scope required"}`,
			wantLatch: false,
		},
		{
			name:      "2xx is not a rejection",
			status:    http.StatusOK,
			body:      forbiddenBody,
			wantLatch: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, latch := ClassifyAccountRejection(tc.status, []byte(tc.body))
			if latch != tc.wantLatch {
				t.Errorf("latch = %v, want %v (reason %q)", latch, tc.wantLatch, reason)
			}
			if len(tc.wantReason) == 0 {
				if reason != "" {
					t.Errorf("reason = %q, want empty", reason)
				}
				return
			}
			for _, want := range tc.wantReason {
				if !strings.Contains(reason, want) {
					t.Errorf("reason %q does not contain %q", reason, want)
				}
			}
		})
	}
}

// rejectingServer serves body with status and records the account headers it
// saw, in order.
type rejectingServer struct {
	mu      sync.Mutex
	headers []string
	hits    atomic.Int64
	status  int
	body    string
}

func (s *rejectingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.hits.Add(1)
	s.mu.Lock()
	s.headers = append(s.headers, r.Header.Get(AccountHeaderName))
	s.mu.Unlock()
	w.WriteHeader(s.status)
	_, _ = w.Write([]byte(s.body))
}

func (s *rejectingServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.headers))
	copy(out, s.headers)
	return out
}

// TestAccountRejection_SecondCallFailsFastLocally proves the latch: an observed
// 403 account_forbidden marks the selection invalid, so the second call is
// refused before dispatch and the server's request count stays at one.
func TestAccountRejection_SecondCallFailsFastLocally(t *testing.T) {
	const id = "acct_01LATCHLATCHLATCHLATCH"

	backend := &rejectingServer{status: http.StatusForbidden, body: forbiddenBody}
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)

	sel := NewAccountSelection(seedSelection(t, t.TempDir(), id), time.Second)
	tr := NewSyncTransport(srv.URL, StaticTokenSource{AccessToken: "tok"}, WithAccountSelection(sel))

	err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01})
	if err == nil {
		t.Fatal("first push: want the gateway rejection, got nil")
	}
	var se *SyncHTTPError
	if !errors.As(err, &se) {
		t.Fatalf("first push: want *SyncHTTPError, got %v", err)
	}
	if !strings.Contains(se.AccountReason, "not a member of the selected Fulminate account") {
		t.Errorf("first push: AccountReason = %q, want the membership remedy", se.AccountReason)
	}
	if got := backend.hits.Load(); got != 1 {
		t.Fatalf("first push: server saw %d requests, want 1", got)
	}

	// Second call: refused locally, never dispatched.
	err = tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01})
	if !errors.Is(err, ErrAccountSelectionRejected) {
		t.Errorf("second push: err = %v, want ErrAccountSelectionRejected", err)
	}
	if got := backend.hits.Load(); got != 1 {
		t.Errorf("second push reached the server: %d requests, want the count to stay at 1", got)
	}
}

// TestAccountRejection_NeverFallsBack proves the ticket's "never silently fall
// back": every inbound request carried exactly the selected id — no header-less
// retry and no retry against a different account.
func TestAccountRejection_NeverFallsBack(t *testing.T) {
	const id = "acct_01NOFALLBACKNOFALLBACK"

	backend := &rejectingServer{status: http.StatusForbidden, body: forbiddenBody}
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)

	sel := NewAccountSelection(seedSelection(t, t.TempDir(), id), time.Second)
	tr := NewSyncTransport(srv.URL, StaticTokenSource{AccessToken: "tok"}, WithAccountSelection(sel))

	_ = tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01})
	_, _ = tr.SyncControlJSON(context.Background(), "presign", []byte(`{}`))

	seen := backend.seen()
	if len(seen) == 0 {
		t.Fatal("the server recorded no requests at all — the assertion below would be vacuous")
	}
	for i, h := range seen {
		if h != id {
			t.Errorf("request %d carried account header %q, want exactly %q — no fallback is permitted", i, h, id)
		}
	}
}

// TestAccountRejection_TransientLookupFailureDoesNotLatch proves a transient
// gateway failure cannot fail-fast a valid account: after a 403
// subscription_required carrying "account lookup failed", the second call still
// reaches the server.
func TestAccountRejection_TransientLookupFailureDoesNotLatch(t *testing.T) {
	const id = "acct_01TRANSIENTTRANSIENTTR"

	backend := &rejectingServer{
		status: http.StatusForbidden,
		body:   `{"error":"subscription_required","error_description":"account lookup failed"}`,
	}
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)

	sel := NewAccountSelection(seedSelection(t, t.TempDir(), id), time.Second)
	tr := NewSyncTransport(srv.URL, StaticTokenSource{AccessToken: "tok"}, WithAccountSelection(sel))

	err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01})
	if err == nil {
		t.Fatal("first push: want the gateway failure, got nil")
	}
	var se *SyncHTTPError
	if !errors.As(err, &se) {
		t.Fatalf("first push: want *SyncHTTPError, got %v", err)
	}
	if !strings.Contains(se.AccountReason, "could not complete the account check") {
		t.Errorf("AccountReason = %q, want the gateway's own reason surfaced", se.AccountReason)
	}

	// Second call still goes out — the selection was NOT latched.
	err = tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01})
	if errors.Is(err, ErrAccountSelectionRejected) {
		t.Error("a transient lookup failure latched the selection; it must not")
	}
	if got := backend.hits.Load(); got != 2 {
		t.Errorf("server saw %d requests, want 2 — the second call must still reach the gateway", got)
	}

	// The subscription-lookup and missing-context descriptions behave the same.
	for _, desc := range []string{"subscription lookup failed", "missing authentication context"} {
		backend2 := &rejectingServer{
			status: http.StatusForbidden,
			body:   `{"error":"subscription_required","error_description":"` + desc + `"}`,
		}
		srv2 := httptest.NewServer(backend2)
		sel2 := NewAccountSelection(seedSelection(t, t.TempDir(), id), time.Second)
		tr2 := NewSyncTransport(srv2.URL, StaticTokenSource{AccessToken: "tok"}, WithAccountSelection(sel2))
		_ = tr2.PushGraph(context.Background(), "knowledge", "default", []byte{0x01})
		_ = tr2.PushGraph(context.Background(), "knowledge", "default", []byte{0x01})
		if got := backend2.hits.Load(); got != 2 {
			t.Errorf("%q: server saw %d requests, want 2", desc, got)
		}
		srv2.Close()
	}

	// Known-positive control: the SAME harness DOES latch on a settled
	// rejection, so the not-latched conclusion above is a real distinction.
	latching := &rejectingServer{status: http.StatusForbidden, body: forbiddenBody}
	srv3 := httptest.NewServer(latching)
	t.Cleanup(srv3.Close)
	sel3 := NewAccountSelection(seedSelection(t, t.TempDir(), id), time.Second)
	tr3 := NewSyncTransport(srv3.URL, StaticTokenSource{AccessToken: "tok"}, WithAccountSelection(sel3))
	_ = tr3.PushGraph(context.Background(), "knowledge", "default", []byte{0x01})
	_ = tr3.PushGraph(context.Background(), "knowledge", "default", []byte{0x01})
	if got := latching.hits.Load(); got != 1 {
		t.Errorf("control: a settled rejection let %d requests through, want 1", got)
	}
}
