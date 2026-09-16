// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestSegmentManager_DestinationBoundChildServesItsOwnAccount is the segment arm
// of the account ladder: a child serving one account keeps serving it while the
// machine-wide selection names another, because its cache directory and its
// bound account ARE that destination's.
//
// IT DRIVES THE CHILD ON THE CTX THAT SELECTED IT, which is the only way
// production reaches one: every serving arm routes through ForDestination(ctx),
// so the destination that chose the child is on the context of the call it then
// serves. A bound child reached on a destination-less context is a shape no
// caller produces, and the guard judges THAT one against the selection — which
// is the mid-session-switch case the guard exists for.
func TestSegmentManager_DestinationBoundChildServesItsOwnAccount(t *testing.T) {
	live := stubAccountSelection(t, "11111111-1111-4111-8111-111111111111")
	root := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	const accountB = "33333333-3333-4333-8333-333333333333"
	boundCtx := graphclient.WithDestination(context.Background(),
		graphclient.Destination{Storage: "cloud", AccountID: accountB})
	boundToB := root.ForDestination(boundCtx)
	if boundToB == root {
		t.Fatal("ForDestination returned the root; the fixture is not exercising a bound child")
	}
	if boundToB.boundAccountID != accountB {
		t.Fatalf("child boundAccountID = %q, want %q", boundToB.boundAccountID, accountB)
	}

	if _, err := boundToB.Search(boundCtx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); err != nil {
		t.Errorf("a child serving account B must serve while the selection names another: %v", err)
	}

	// …and keeps serving however the selection moves, including onto its own
	// account and off it. The child's identity is the request's destination, not
	// the selection, so no movement of the selection is an event for it.
	for _, selection := range []string{accountB, "44444444-4444-4444-8444-444444444444", ""} {
		*live = selection
		if _, err := boundToB.Search(boundCtx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); err != nil {
			t.Errorf("selection %q: %v", selection, err)
		}
	}

	// A LOCAL destination stays exempt on its own terms — the row exists so a
	// guard widened to cover local storage is caught here.
	*live = "11111111-1111-4111-8111-111111111111"
	localCtx := graphclient.WithDestination(context.Background(), graphclient.Destination{Storage: "local"})
	boundLocal := root.ForDestination(localCtx)
	if _, err := boundLocal.Search(localCtx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); err != nil {
		t.Errorf("a local-bound child must never consult the account selection: %v", err)
	}
}

// TestSegmentManager_RootStillRefusesAfterAFlip is the control for the row
// above: the construction-time guard is still armed on the ROOT manager, which
// is the manager that really does sample its cache root once, and on the
// unbound call that follows the selection.
func TestSegmentManager_RootStillRefusesAfterAFlip(t *testing.T) {
	live := stubAccountSelection(t, "11111111-1111-4111-8111-111111111111")
	root := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	ctx := context.Background()

	if _, err := root.Search(ctx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); err != nil {
		t.Fatalf("known-positive control before the flip: %v", err)
	}
	*live = "33333333-3333-4333-8333-333333333333"
	if _, err := root.Search(ctx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("root after the flip: err = %v, want ErrAccountChanged", err)
	}
}

// TestSegmentManager_ARestartLessSwitchIsServedByTheNewAccountsChild is the
// SEGMENT leg of the restart-less account switch.
//
// manage(account_use) moves the machine-wide selection and restarts nothing, so
// the root Manager keeps the boundAccountID it sampled at construction. This
// pins what that means for a read taken after the switch: the request binds the
// NEW account, ForDestination serves it from that account's own child over that
// account's own cache directory, and the guard passes — the root manager is
// never the thing serving it.
//
// The refusal the root manager still makes is asserted alongside, because that
// is the true remaining state: BACKGROUND work, which binds no destination and
// follows the selection, is what the guard is left for, and its message no
// longer promises a restart the daemon does not perform.
//
// WHERE THIS ROW'S DISCRIMINATING POWER IS, stated so it is not overclaimed:
// the child-serves leg holds here by construction (ForDestination partitions by
// account, and after the switch the child's account and the selection agree),
// so it does not red on a guard mutation — TestSegmentManager_DestinationBoundChildServesItsOwnAccount
// above is the row that does, by serving a child bound to the account the
// selection has moved OFF. What reds here is the pair this row exists for: the
// root refusing, and the remedy text no longer promising a restart.
func TestSegmentManager_ARestartLessSwitchIsServedByTheNewAccountsChild(t *testing.T) {
	const (
		accountA = "11111111-1111-4111-8111-111111111111"
		accountB = "33333333-3333-4333-8333-333333333333"
	)
	live := stubAccountSelection(t, accountA)
	root := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	// Known-positive control: before the switch, the daemon's own unbound work
	// serves from the root manager.
	ctx := context.Background()
	if _, err := root.Search(ctx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); err != nil {
		t.Fatalf("before the switch the root manager serves: %v", err)
	}

	// THE SWITCH, with no restart: the selection moves and this process, this
	// Manager and this root all stay exactly as they were.
	*live = accountB

	// A user read after the switch binds the new account and is served by ITS
	// child, over its own cache directory.
	boundCtx := graphclient.WithDestination(ctx, graphclient.Destination{Storage: "cloud", AccountID: accountB})
	child := root.ForDestination(boundCtx)
	if child == root {
		t.Fatal("the switched-to account must be served by its own child, not by the root manager")
	}
	if child.boundAccountID != accountB {
		t.Fatalf("child boundAccountID = %q, want the switched-to account %q", child.boundAccountID, accountB)
	}
	if _, err := child.Search(boundCtx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); err != nil {
		t.Errorf("a read after a restart-less switch must be served: %v", err)
	}

	// And the root manager — the daemon's own unbound work — is what is left
	// holding the previous selection, with a message that names no restart the
	// daemon performs.
	_, err := root.Search(ctx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5)
	if !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("the root manager after the switch: err = %v, want ErrAccountChanged", err)
	}
	if strings.Contains(err.Error(), "the daemon restarts itself") {
		t.Errorf("the remedy still promises a restart the daemon does not perform: %v", err)
	}
	for _, want := range []string{accountA, accountB, "knowledge stop", "knowledge start"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}
