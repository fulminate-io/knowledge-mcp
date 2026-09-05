// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestBM25DropCensusRendersOnBothOperatorSurfaces gates the OPERATOR-FACING half
// of the carrier: both surfaces name a drop, both are byte-identical to what they
// were before the census existed when nothing dropped, and the rebuild response
// reports THIS RUN rather than a stale total.
func TestBM25DropCensusRendersOnBothOperatorSurfaces(t *testing.T) {
	// dropRow is one probed coverage row. Every subtest below starts from this
	// exact value so a rendered difference is attributable to the field it changed.
	dropRow := func(census map[string]int) CoverageRow {
		return CoverageRow{
			Graph: "code/myrepo", Total: 10, Summarized: 10, Embedded: 10,
			SegCovered: 10, LiveResident: 10, HasSegments: true, SegProbed: true,
			Degraded: census,
		}
	}

	t.Run("the rebuild response names the drop", func(t *testing.T) {
		got := rebuildDegradeSentence(RebuildOutcome{Degraded: map[string]int{"tokenize_panic": 2}})
		require.Equal(t,
			" WARNING: this rebuild DROPPED input it could not index — tokenize_panic 2."+
				" Those documents are not searchable in the corpus this run published.",
			got)
	})

	t.Run("a clean rebuild response is byte-identical to before the census existed", func(t *testing.T) {
		require.Empty(t, rebuildDegradeSentence(RebuildOutcome{Degraded: nil}))
		require.Empty(t, rebuildDegradeSentence(RebuildOutcome{Degraded: map[string]int{}}),
			"an empty census and no census are one state")
		require.Empty(t, rebuildDegradeSentence(RebuildOutcome{Degraded: map[string]int{"tokenize_panic": 0}}),
			"a non-positive count is not a drop")
	})

	t.Run("the coverage cell names the drop", func(t *testing.T) {
		require.Equal(t, " · dropped (tokenize_panic 2)",
			coverageDegradeSuffix(dropRow(map[string]int{"tokenize_panic": 2})))
	})

	// THE STRONGEST LEG: one row, ONE field changed, and the output must differ by
	// EXACTLY the suffix — so the suffix is attributable to the census and to
	// nothing else about the row.
	t.Run("a clean coverage cell is byte-identical and the suffix is attributable to the census alone", func(t *testing.T) {
		clean := segmentCoverageCell(dropRow(nil))
		require.NotContains(t, clean, "dropped", "a row with no drops renders as it did before the census existed")

		dirty := segmentCoverageCell(dropRow(map[string]int{"tokenize_panic": 2}))
		require.Equal(t, clean+" · dropped (tokenize_panic 2)", dirty)
	})

	t.Run("the census orders count-descending then name-ascending", func(t *testing.T) {
		census := map[string]int{"tokenize_panic": 2, "aaa_other": 7, "zzz_other": 7}
		// aaa_other and zzz_other tie at 7 and break by name; tokenize_panic is last
		// on count. A name-only or insertion order would fail here.
		require.Equal(t, "aaa_other 7, zzz_other 7, tokenize_panic 2", degradeClassList(census))
		require.Contains(t, rebuildDegradeSentence(RebuildOutcome{Degraded: census}), "— aaa_other 7, zzz_other 7, tokenize_panic 2.",
			"both surfaces carry the one ordering")
	})

	// THE ORDERING LEG. A STALE census is scripted onto the fake before the run and
	// the finalize's own builds contribute a different one. A driver that cleared
	// AFTER the finalize reports nothing; one that never cleared reports the sum.
	// Only clearing BEFORE the finalize reports this run's drops alone.
	t.Run("the rebuild reports THIS RUN's drops, proving the clear ran before the finalize", func(t *testing.T) {
		shipper := &fakeRebuildShipper{}
		shipper.degradeCensus = map[string]int{"tokenize_panic": 99} // stale, from an earlier window
		shipper.finalizeCensus = map[string]int{"tokenize_panic": 2} // what THIS run's builds drop

		out, err := RebuildSegments(context.Background(), twoBucketScanner(), shipper,
			kgtypes.GraphCode, "drop-census-order", true)
		require.NoError(t, err)
		require.True(t, out.Published, "the swap must land, or the response below describes a run that did nothing")

		require.Equal(t, 1, shipper.degradeResets, "the driver must clear the census exactly once")
		require.Equal(t, map[string]int{"tokenize_panic": 2}, out.Degraded,
			"the response must carry THIS run's drops, not the stale total and not nothing")
	})
}
