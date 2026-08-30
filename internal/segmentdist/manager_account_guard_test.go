// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"strings"
	"testing"

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
