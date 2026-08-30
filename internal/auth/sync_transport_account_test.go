// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// accountTestTransport builds a Transport against srv with an injected account
// selection over a temp config carrying id (or none when id is "").
func accountTestTransport(t *testing.T, srv *httptest.Server, id string) *Transport {
	t.Helper()
	sel := NewAccountSelection(seedSelection(t, t.TempDir(), id), time.Second)
	src := StaticTokenSource{AccessToken: "tok", Permissions: PermissionSet{PermMCPKnowledgeWrite: {}}}
	return NewSyncTransport(srv.URL, src, WithAccountSelection(sel))
}

// TestAccountHeader_SyncPushTransport asserts the first of the two wire
// surfaces behind issueBytes: a sync PUSH carries Knowledge-Account-Id when a
// selection is set, and carries NO such header at all when none is.
func TestAccountHeader_SyncPushTransport(t *testing.T) {
	const id = "acct_01PUSHPUSHPUSHPUSHPUSHP"

	var gotHeader string
	var headerPresent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, headerPresent = r.Header[http.CanonicalHeaderKey(AccountHeaderName)]
		gotHeader = r.Header.Get(AccountHeaderName)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// Selection set: the header is stamped.
	tr := accountTestTransport(t, srv, id)
	if err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01}); err != nil {
		t.Fatalf("PushGraph(with selection): %v", err)
	}
	if gotHeader != id {
		t.Errorf("push: %s = %q, want %q", AccountHeaderName, gotHeader, id)
	}

	// No selection: the header is ABSENT, not empty — that is what preserves
	// today's gateway behavior of resolving the caller's primary account.
	gotHeader, headerPresent = "", false
	trNone := accountTestTransport(t, srv, "")
	if err := trNone.PushGraph(context.Background(), "knowledge", "default", []byte{0x01}); err != nil {
		t.Fatalf("PushGraph(no selection): %v", err)
	}
	if headerPresent {
		t.Errorf("push without a selection sent %s = %q; the header must be absent", AccountHeaderName, gotHeader)
	}
}

// TestAccountHeader_SyncControlJSON asserts the control-JSON entry point
// independently of push: a /v1/sync/* control POST stamps the same header under
// the same conditions. PushGraph and SyncControlJSON are separate Transport
// methods, and missing the header on either silently splits a user's data across
// two accounts, so each is asserted on its own.
//
// RENAMED WITH THE CHANNEL IT USED TO NAME. Its predecessor was named for the
// segment control channel, which is deleted; the property is account-header
// stamping, which is channel-independent, so the test survives under a name that
// does not claim a route the transport no longer has.
func TestAccountHeader_SyncControlJSON(t *testing.T) {
	const id = "acct_01SEGSEGSEGSEGSEGSEGSEG"

	var gotHeader, gotPath string
	var headerPresent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, headerPresent = r.Header[http.CanonicalHeaderKey(AccountHeaderName)]
		gotHeader = r.Header.Get(AccountHeaderName)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	tr := accountTestTransport(t, srv, id)
	if _, err := tr.SyncControlJSON(context.Background(), "presign", []byte(`{}`)); err != nil {
		t.Fatalf("SyncControlJSON(with selection): %v", err)
	}
	if gotPath != "/v1/sync/presign" {
		t.Errorf("path = %q, want /v1/sync/presign", gotPath)
	}
	if gotHeader != id {
		t.Errorf("control JSON: %s = %q, want %q", AccountHeaderName, gotHeader, id)
	}

	gotHeader, headerPresent = "", false
	trNone := accountTestTransport(t, srv, "")
	if _, err := trNone.SyncControlJSON(context.Background(), "presign", []byte(`{}`)); err != nil {
		t.Fatalf("SyncControlJSON(no selection): %v", err)
	}
	if headerPresent {
		t.Errorf("control-JSON call without a selection sent %s = %q; the header must be absent", AccountHeaderName, gotHeader)
	}
}

// TestAccountSelection_RefusesBeforeDispatch pins the CEO's "client shouldnt
// try things we know would fail": once a rejection has been observed for the
// stored selection, the round trip is never issued — the test server records
// zero inbound requests.
func TestAccountSelection_RefusesBeforeDispatch(t *testing.T) {
	const id = "acct_01REFUSEREFUSEREFUSERE"

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	sel := NewAccountSelection(seedSelection(t, t.TempDir(), id), time.Second)
	src := StaticTokenSource{AccessToken: "tok", Permissions: PermissionSet{PermMCPKnowledgeWrite: {}}}
	tr := NewSyncTransport(srv.URL, src, WithAccountSelection(sel))

	// Known-positive control: before the rejection, the very same call DOES
	// reach the server — so the zero below means "refused", not "misconfigured
	// test that could never have reached the server".
	if err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01}); err != nil {
		t.Fatalf("PushGraph(before rejection): %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("before rejection: server saw %d requests, want 1", got)
	}

	sel.MarkInvalid(id, "account_forbidden: you are not a member of this account")

	err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01})
	if err == nil {
		t.Fatal("PushGraph(after rejection): want error, got nil")
	}
	if !errors.Is(err, ErrAccountSelectionRejected) {
		t.Errorf("error = %v, want ErrAccountSelectionRejected", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("after rejection: server saw %d requests, want the refusal to stop at 1", got)
	}

	// The same refusal governs the control-JSON surface.
	if _, err := tr.SyncControlJSON(context.Background(), "presign", []byte(`{}`)); !errors.Is(err, ErrAccountSelectionRejected) {
		t.Errorf("control JSON after rejection: err = %v, want ErrAccountSelectionRejected", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("after control-JSON refusal: server saw %d requests, want 1", got)
	}
	if err != nil && !strings.Contains(err.Error(), id) {
		t.Errorf("refusal error %q does not name the rejected account", err)
	}
}
