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

// stubAccountSelection installs a mutable live-account seam and restores the
// production reader on cleanup, so no test here touches a real config.
func stubAccountSelection(t *testing.T, initial string) *string {
	t.Helper()
	prev := accountSelectionID
	live := initial
	accountSelectionID = func(context.Context) string { return live }
	t.Cleanup(func() { accountSelectionID = prev })
	return &live
}

// TestSegmentManagerServesTheRequestBoundAccount is requirement 4's segment
// leg: a request that names its own account is served by that account's own
// child manager, rather than refused because the PROCESS selection is somebody
// else. The child's cache is already partitioned per account, so the refusal
// protected nothing here — it compared the wrong two things.
func TestSegmentManagerServesTheRequestBoundAccount(t *testing.T) {
	live := stubAccountSelection(t, "11111111-1111-4111-8111-111111111111")

	root := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	// A request bound to ANOTHER account: its own child serves it.
	bound := graphclient.WithDestination(context.Background(),
		graphclient.Destination{Storage: "cloud", AccountID: "22222222-2222-4222-8222-222222222222"})
	child := root.ForDestination(bound)
	if child == root {
		t.Fatal("a bound destination must get its own child manager")
	}
	if _, err := child.Search(bound, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); err != nil {
		t.Fatalf("search on the request's own account: %v", err)
	}
	if err := child.Flush(bound, kgtypes.GraphKnowledge, "default"); err != nil {
		t.Fatalf("flush on the request's own account: %v", err)
	}

	// A local binding is exempt exactly as it was.
	local := graphclient.WithDestination(context.Background(), graphclient.Destination{Storage: "local"})
	if _, err := root.ForDestination(local).Search(local, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); err != nil {
		t.Fatalf("search on a local binding: %v", err)
	}

	// THE GUARD STILL FIRES for the case it exists for: an UNBOUND call whose
	// process selection has moved off the account this manager was built under.
	*live = "33333333-3333-4333-8333-333333333333"
	if _, err := root.Search(context.Background(), kgtypes.GraphKnowledge, "default", "alpha", nil, 5); !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("unbound search after the switch: err = %v, want ErrAccountChanged", err)
	}

	// ...and the guard itself refuses a binding that disagrees with the manager
	// it is asked about: a child serves ITS account, never another's. (A real
	// serving call never gets that far — Search re-resolves ForDestination from
	// the same ctx and lands on the right child — so the guard is asserted
	// directly, which is where the fail-closed property lives.)
	other := graphclient.WithDestination(context.Background(),
		graphclient.Destination{Storage: "cloud", AccountID: "44444444-4444-4444-8444-444444444444"})
	if err := child.checkAccountBinding(other); !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("a child asked about another account's binding: err = %v, want ErrAccountChanged", err)
	}
}

// TestSegmentManagerRefusesAfterAccountFlip proves the fail-closed backstop: a
// Manager built under one account refuses to serve once the selection moves,
// with an actionable remedy, instead of returning the previous account's
// cached segments.
func TestSegmentManagerRefusesAfterAccountFlip(t *testing.T) {
	ctx := context.Background()
	live := stubAccountSelection(t, "acct_01AAA")

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	// Known-positive control: before the switch the SAME calls succeed, so the
	// refusals below are the switch's doing and not a broken fixture.
	if _, err := mgr.Search(ctx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); err != nil {
		t.Fatalf("search before the switch: %v", err)
	}
	if err := mgr.Flush(ctx, kgtypes.GraphKnowledge, "default"); err != nil {
		t.Fatalf("flush before the switch: %v", err)
	}

	// The user switches accounts in another process.
	*live = "acct_01BBB"

	_, err := mgr.Search(ctx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5)
	if !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("search after the switch: err = %v, want ErrAccountChanged", err)
	}
	for _, want := range []string{"acct_01AAA", "acct_01BBB", "knowledge stop", "knowledge start", "brew services restart knowledge"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}

	if err := mgr.Flush(ctx, kgtypes.GraphKnowledge, "default"); !errors.Is(err, ErrAccountChanged) {
		t.Errorf("flush after the switch: err = %v, want ErrAccountChanged", err)
	}
	if err := mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphKnowledge, "default"); !errors.Is(err, ErrAccountChanged) {
		t.Errorf("re-emit after the switch: err = %v, want ErrAccountChanged", err)
	}

	// Switching BACK to the account the manager was built under serves again —
	// the guard compares identity, it does not latch.
	*live = "acct_01AAA"
	if _, err := mgr.Search(ctx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); err != nil {
		t.Errorf("search after switching back: %v", err)
	}

	// A manager built with NO selection is unaffected while none is selected.
	*live = ""

	unbound := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	if _, err := unbound.Search(ctx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); err != nil {
		t.Errorf("search with no selection at all: %v", err)
	}
	// ...and refuses once one is established under it.
	*live = "acct_01NEW"
	if _, err := unbound.Search(ctx, kgtypes.GraphKnowledge, "default", "alpha", nil, 5); !errors.Is(err, ErrAccountChanged) {
		t.Errorf("establishing a selection mid-session: err = %v, want ErrAccountChanged", err)
	}
}
