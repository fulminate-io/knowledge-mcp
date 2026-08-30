// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// segment_delta_two_pass_test.go — the property the whole watermark split exists
// for, asserted end to end: the same rows stop re-qualifying once they are merged.
//
// WHY IT NEEDS ITS OWN TEST. Every other gate on this change asserts what the client
// PUTS ON THE WIRE. None of them can see a SECOND pass, because the value under test
// only matters once a first pass has moved a horizon. Before the split the delta arm
// sent one field carrying both meanings, so its window widened back down to whichever
// consumer lagged — pinned at the rebuild watermark — and every pass re-served and
// re-merged exactly the rows the pass before it had merged, forever.

// twoPassCorpusN is the fixture's seeded row count, stated as a constant so the
// non-zero assertion below compares against the FIXTURE rather than against a number
// read back out of the run it is supposed to be checking.
const twoPassCorpusN = 4

// TestSegmentDeltaTwoPassesConvergeOnce drives two real delta passes over an
// unchanged corpus and requires the second to merge nothing.
func TestSegmentDeltaTwoPassesConvergeOnce(t *testing.T) {
	const repo = "twopassrepo"
	const seeded = int64(2_000_000_000)

	c, eng := buildSeedRebuildClientAt(t, t.TempDir(), repo)
	g := segmentGraphRef{gt: kgtypes.GraphCode, name: repo}

	// The rebuild position sits BELOW the merge horizon and never moves. That is the
	// pinned-lagging-consumer state the defect lived in: with one field carrying both
	// meanings, every pass re-read from here.
	require.NoError(t, c.segmentMgr.SaveRebuildState(kgtypes.GraphCode, repo, 1_000_000_000, nil))
	require.NoError(t, c.segmentMgr.SaveMergeWatermark(kgtypes.GraphCode, repo, seeded))

	corpus := make([]stampedNode, 0, twoPassCorpusN)
	for i := range twoPassCorpusN {
		corpus = append(corpus, stampedNode{
			id:        repo + "-node-" + string(rune('a'+i)),
			stampedAt: seeded + int64(i) + 1,
			vector:    seedFixtureVector(byte('a' + i)),
		})
	}
	servedHorizon := seeded + int64(twoPassCorpusN) + 10
	eng.mu.Lock()
	eng.corpus, eng.servedHorizon = corpus, servedHorizon
	eng.mu.Unlock()

	ctx := context.Background()

	first := c.consumeSegmentDelta(ctx, g, nil)
	require.Equal(t, twoPassCorpusN, first.Merged,
		"VACUITY GUARD, and the third assertion proves nothing without it: pass one must merge the "+
			"fixture's %d seeded rows. A zero here would make pass two's zero mean an empty corpus rather "+
			"than convergence", twoPassCorpusN)
	c.commitMergeWatermark(g, first)

	beforeSecond, _ := eng.recorded()

	second := c.consumeSegmentDelta(ctx, g, nil)
	require.Zero(t, second.Merged,
		"pass two over an UNCHANGED corpus must merge nothing. Before the split it re-merged all %d rows "+
			"every pass, because the single field collapsed the window back down to the pinned rebuild "+
			"position", twoPassCorpusN)

	// THE CONVERGENCE MUST BE ATTRIBUTABLE TO THE NEW FIELD, not to an empty corpus
	// or a pass that never ran.
	all, unexpected := eng.recorded()
	require.Greater(t, len(all), len(beforeSecond), "pass two must actually have issued a scan")
	require.Equal(t, servedHorizon, all[len(beforeSecond)].ScanFromStampedAtNanos,
		"and pass two's first request must be bounded at pass one's SERVED HORIZON, which is the value the "+
			"commit carried forward — that field is what makes the merged rows stop re-qualifying")
	require.Equal(t, int64(1_000_000_000), all[len(beforeSecond)].AfterStampedAtNanos,
		"while the retention field still reports the LAGGING rebuild position, unchanged: the two meanings "+
			"travel separately, and this is the half that must NOT advance")
	require.Empty(t, unexpected,
		"no assertion above may be holding because the arm read a different axis")
}
