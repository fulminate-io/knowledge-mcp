// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// deletion_refusal_test.go — the refusal notice's TWO shapes.
//
// WHY THE ZERO-NAMED SHAPE IS ITS OWN CASE. A refusal reaches the client on two
// different collects: a DIFF collect, which named entries the server refused, and
// a FULL collect, whose deletion basis the server DERIVED and where the client
// named nothing at all. One sentence cannot serve both — "the 0 entries this
// collect named as deleted are STILL in the graph" is what the second reads as,
// and it tells the operator nothing true.

// TestDeletionRefusalNotice_NamedAndDerivedShapes pins both branches and the
// silent case in one run.
func TestDeletionRefusalNotice_NamedAndDerivedShapes(t *testing.T) {
	// (a) A DIFF collect: the client named entries, so the count is the useful fact.
	ctx, named := WithDeletionRefusal(context.Background())
	RecordDeletionRefusal(ctx, "manifest_identity_mismatch", 201)
	got := named.Notice()
	require.Contains(t, got, "manifest_identity_mismatch", "the notice must name the guard that refused")
	require.Contains(t, got, "201 entries this collect named",
		"a client-named refusal reports how many entries are still in the graph")

	// (b) A FULL collect: the basis was server-DERIVED and the client named NOTHING.
	// The notice must not report a count at all.
	dctx, drec := WithDeletionRefusal(context.Background())
	RecordDeletionRefusal(dctx, "walk_incomplete", 0)
	derived := drec.Notice()
	require.Contains(t, derived, "walk_incomplete")
	require.NotContains(t, derived, "0 entries",
		"a DERIVED refusal named no entries, so a count of zero is a false statement about what was refused")
	require.Contains(t, derived, "deletion phase did not run",
		"the zero-named shape says what actually happened: the phase did not run")
	require.Contains(t, derived, "Nothing was destroyed",
		"both shapes must keep the recovery sentence — it is what the operator acts on")

	// (c) THE SILENT CASE, which is what makes (a) and (b) evidence of a branch
	// rather than of a function that always speaks: an admitted deletion renders
	// nothing, and so does a nil recorder.
	_, admitted := WithDeletionRefusal(context.Background())
	require.Empty(t, admitted.Notice(), "an admitted deletion renders no notice")
	var absent *DeletionRefusal
	require.Empty(t, absent.Notice(), "a nil recorder renders no notice rather than panicking")
}
