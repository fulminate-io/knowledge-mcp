// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// mergeDeltaWindow is one window's worth of LIVE items, ids "md00000000"..
const mergeDeltaWindow = 4

// TestMergeSegmentDeltaReturnsLiveItems pins that the merge CONSUMES the live half of
// the window rather than discarding it, and reports how much it landed.
//
// The live half was read by this same scan for as long as the feed has existed and
// thrown away at the bind — so the reported count is the difference between a
// delete-only consumer and the currency path.
func TestMergeSegmentDeltaReturnsLiveItems(t *testing.T) {
	ctx := context.Background()
	const since = int64(1_600_000_000_000_000_000)
	const horizon = int64(1_700_000_000_123_456_789)

	sc := &fakeRepairScanner{
		pages:   [][]*knowledgev1.PipelineScanItem{makeScanPage("md", 0, mergeDeltaWindow)},
		horizon: horizon,
	}
	shipper := &fakeRebuildShipper{}
	merger := &fakeRepairShipper{}

	out, err := MergeSegmentDelta(ctx, sc, shipper, merger, nil, kgtypes.GraphCode, "myrepo", since)
	require.NoError(t, err)

	require.Equal(t, mergeDeltaWindow, out.Merged,
		"every live item in the window is merged — the count is a fixture-derived constant, not whatever the code computed")
	require.Equal(t, horizon, out.Horizon,
		"the server-served horizon is reported so the caller can commit it after its drain")
	require.Zero(t, out.Learned, "this window reported no deletes")

	// THE WINDOW IS SCOPED BY THE CALLER'S HORIZON, which is what keeps this a delta.
	// A merge that asked from zero would be a whole-corpus read wearing a delta's name.
	require.NotEmpty(t, sc.afterStamped)
	require.Equal(t, since, sc.afterStamped[0],
		"the read starts from the horizon the caller passed, not from zero and not from a local clock")
}

// TestMergeSegmentDeltaBuildsDocumentsForEveryItem is the NO-MEMBERSHIP-FILTER
// catcher, and it is the trap this path most invites.
//
// It is tempting to ask UncoveredMembers first and merge only what is missing — that
// is what the repair arm does, and it is WRONG here: a co-worker's UPDATE to an
// existing node produces an id that IS already live-resident, carrying a NEW vector,
// so a presence filter would drop exactly the updates this path exists to deliver.
// The fixture's shipper reports NOTHING missing, which is precisely the shape a
// filtered implementation would merge zero documents for.
func TestMergeSegmentDeltaBuildsDocumentsForEveryItem(t *testing.T) {
	ctx := context.Background()

	sc := &fakeRepairScanner{
		pages: [][]*knowledgev1.PipelineScanItem{makeScanPage("md", 0, mergeDeltaWindow)},
	}
	shipper := &fakeRebuildShipper{}
	// missingHNSW / missingBM25 both empty: every id is already live-resident.
	merger := &fakeRepairShipper{}

	out, err := MergeSegmentDelta(ctx, sc, shipper, merger, nil, kgtypes.GraphCode, "myrepo", 1)
	require.NoError(t, err)
	require.Equal(t, mergeDeltaWindow, out.Merged)

	require.Len(t, merger.hnswDocs, mergeDeltaWindow,
		"EVERY item is built, including ids already resident — a membership filter here drops co-worker updates")
	require.Len(t, merger.fieldDocs, mergeDeltaWindow,
		"and the BM25 half likewise")
	require.Zero(t, merger.reEmitCalls,
		"the merge only marks partitions dirty; the CALLER's drain ships them, which is what the two-part commit depends on")

	// The membership probe is not consulted at all — the assertion above would also
	// hold if it were consulted and happened to report everything missing, so pin the
	// stronger property.
	require.Empty(t, merger.probedIDs,
		"the merge must not ask which ids are already covered — that question is the repair arm's, and the wrong one here")
}

// TestRepairOutcomeCarriesServedHorizon pins the field the backstop's cold-start
// bridge reads: the repair's unwatermarked scan IS the honest reading of "current"
// for a graph, and it must reach the caller rather than being discarded at the bind.
func TestRepairOutcomeCarriesServedHorizon(t *testing.T) {
	ctx := context.Background()
	const horizon = int64(1_700_000_000_123_456_789)

	sc := newRepairFixture()
	sc.horizon = horizon
	sh := &fakeRepairShipper{}

	out, err := RepairUncoveredSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo")
	require.NoError(t, err)
	require.True(t, out.Ran)
	require.Equal(t, horizon, out.ServedHorizonNanos,
		"the horizon the scan was served must reach the caller — it is what seeds a scanned graph's merge window")

	// KNOWN NEGATIVE on the same probe: a scan served no horizon reports zero, and the
	// caller's `> 0` guard is what stops a zero re-arming a whole-corpus pull.
	scZero := newRepairFixture()
	outZero, err := RepairUncoveredSegments(ctx, scZero, &fakeRepairShipper{}, kgtypes.GraphCode, "otherrepo")
	require.NoError(t, err)
	require.Zero(t, outZero.ServedHorizonNanos,
		"a scan served no horizon reports zero rather than inventing one")
}
