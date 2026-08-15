// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const accountsBody = `{"accounts":[{"id":"acct_01LIST","name":"Acme","slug":"acme","role":"owner","has_active_subscription":true}],"count":1}`

// TestTransport_ListAccounts_GetsMeAccountsWithJSONAccept pins the request
// shape and the verbatim body return.
func TestTransport_ListAccounts_GetsMeAccountsWithJSONAccept(t *testing.T) {
	const id = "acct_01LISTLISTLISTLISTLIST"

	var gotMethod, gotPath, gotAccept, gotAuth, gotAccount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAccept = r.Header.Get("Accept")
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get(AccountHeaderName)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(accountsBody))
	}))
	t.Cleanup(srv.Close)

	sel := NewAccountSelection(seedSelection(t, t.TempDir(), id), time.Second)
	tr := NewSyncTransport(srv.URL, StaticTokenSource{AccessToken: "tok"}, WithAccountSelection(sel))

	body, err := tr.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/v1/me/accounts" {
		t.Errorf("path = %q, want /v1/me/accounts", gotPath)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok")
	}
	// A valid selection is still stamped — only the refusal is bypassed.
	if gotAccount != id {
		t.Errorf("%s = %q, want %q", AccountHeaderName, gotAccount, id)
	}
	if string(body) != accountsBody {
		t.Errorf("body = %q, want it returned verbatim", string(body))
	}
}

// TestTransport_ListAccounts_NotBlockedByInvalidSelection proves the recovery
// command is never locked out: a known-invalid selection refuses a push but
// still lets the list call through.
func TestTransport_ListAccounts_NotBlockedByInvalidSelection(t *testing.T) {
	const id = "acct_01RECOVERRECOVERRECOVE"

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(accountsBody))
	}))
	t.Cleanup(srv.Close)

	sel := NewAccountSelection(seedSelection(t, t.TempDir(), id), time.Second)
	tr := NewSyncTransport(srv.URL, StaticTokenSource{AccessToken: "tok"}, WithAccountSelection(sel))

	sel.MarkInvalid(id, "account_forbidden: you are not a member of this account")

	// Known-positive control for the refusal being live at all: a push IS
	// refused under exactly this state, so the list call's success below is a
	// deliberate bypass rather than a marker that never armed.
	if err := tr.PushGraph(context.Background(), "knowledge", "default", []byte{0x01}); err == nil {
		t.Fatal("push under a rejected selection must be refused")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("the refused push reached the server: %d requests", got)
	}

	body, err := tr.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts under a rejected selection: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server saw %d list requests, want 1", got)
	}
	if string(body) != accountsBody {
		t.Errorf("body = %q, want the accounts payload", string(body))
	}
}
