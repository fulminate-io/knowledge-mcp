// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// TestRejectionExpiresAfterTheTTL is the membership-lag arm. A 403 for an
// account is a fact about a MOMENT: the user is added to the account a minute
// later and the gateway would admit them, but the daemon kept refusing locally
// until it was restarted — and the remedy it printed told the user to change
// the GLOBAL selection, which is not what a header-named account needs.
//
// The record now expires. Below the TTL the call is still refused without a
// round trip; past it the gateway is asked again and answers for itself.
func TestRejectionExpiresAfterTheTTL(t *testing.T) {
	const account = "22222222-2222-4222-8222-222222222222"

	sel, clk := newClockedSelection(t, seedSelection(t, t.TempDir(), "11111111-1111-4111-8111-111111111111"))
	sel.MarkInvalid(account, "account_forbidden: you are not a member of this account")

	if err := sel.RejectionFor(account); err == nil {
		t.Fatal("a freshly observed rejection must refuse the next call for that account")
	}

	// Still inside the window: the client does not re-ask.
	clk.advance(RejectedAccountTTL / 2)
	if err := sel.RejectionFor(account); err == nil {
		t.Fatal("inside the TTL the rejection must still refuse: the client does not retry what it just watched fail")
	}

	// Past the window: the gateway is the authority again.
	clk.advance(RejectedAccountTTL)
	if err := sel.RejectionFor(account); err != nil {
		t.Fatalf("past the TTL the account must be tried again, got %v", err)
	}
}

// TestRejectionExpiryLeavesTheSelectionClearingPathAlone pins that the TTL is an
// ADDITION: a deliberate account switch still drops the whole record within one
// selection TTL, with no IPC, exactly as before.
func TestRejectionExpiryLeavesTheSelectionClearingPathAlone(t *testing.T) {
	const selected = "11111111-1111-4111-8111-111111111111"
	const other = "33333333-3333-4333-8333-333333333333"
	ctx := t.Context()

	path := seedSelection(t, t.TempDir(), selected)
	sel, clk := newClockedSelection(t, path)
	if _, err := sel.IDForRequest(ctx); err != nil {
		t.Fatalf("seed read: %v", err)
	}
	sel.MarkInvalid(selected, "account_forbidden: you are not a member of this account")
	if _, err := sel.IDForRequest(ctx); err == nil {
		t.Fatal("the rejected selection must be refused")
	}

	if err := config.WriteSelectedAccountID(path, other); err != nil {
		t.Fatalf("switch selection: %v", err)
	}
	clk.advancePastTTL()
	if got, err := sel.IDForRequest(ctx); err != nil || got != other {
		t.Fatalf("after a deliberate switch: id=%q err=%v, want %q and no error", got, err, other)
	}
	// ...and the record it carried is gone rather than merely expired.
	if err := sel.RejectionFor(selected); err != nil {
		t.Fatalf("a deliberate switch drops the whole record: %v", err)
	}
}
