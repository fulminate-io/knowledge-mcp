// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// densify_topk_test.go covers the per-member neighbor selection inside
// selectTopicKNN on two axes that no other densify test touches.
//
// THE TIE AXIS. The selection keeps each member's k best co-members by
// similarity, and the k-th place is decided by a TIE-BREAK whenever more
// candidates share a score than there are slots. Every other densify test uses
// distinct scores, so the tie-break is unpinned there and a selector swap could
// change which neighbors survive without turning anything red.
//
// THE COST AXIS. The selection's allocation must scale with the pairs it SELECTS
// (members * k), not with the pairs it COMPARES (members squared). Materializing
// every above-threshold candidate per member and sorting the list is the shape
// this gate exists to make impossible; the red run is recorded in
// testdata/redfirst_densify_topk.txt.

// knnVectorBytes is the vector width BitSimilarity requires; a slice of any other
// length scores 0 and would be silently skipped, making every fixture below empty.
const knnVectorBytes = 32

// knnVector builds a 256-bit vector with exactly the given bit positions set. It
// is the whole fixture language here: BitSimilarity is 1 - hamming/256, so two
// vectors differing in d bit positions score exactly 1 - d/256, and a score TIE is
// built by giving several partners the SAME hamming distance from a member while
// differing from each other.
func knnVector(bits ...int) []byte {
	v := make([]byte, knnVectorBytes)
	for _, b := range bits {
		v[b/8] |= 1 << (b % 8)
	}
	return v
}

// newTieFixture builds one topic in which EVERY per-member candidate list has a
// tie that spans the k-th slot, so every selection the function makes is decided
// by the tie-break rather than by the scores.
//
//   - m0 is the all-zero vector. p1..p5 each differ from it in ONE distinct bit,
//     so all five partners score EXACTLY 1-1/256 against m0: a five-way tie for
//     two slots.
//   - Each pi differs from each pj in TWO bits, so every pi sees m0 first
//     (1-1/256) and then a four-way tie at 1-2/256 for its remaining slot.
//
// Both score levels sit above the threshold the test passes, so the threshold
// filter admits everything and the cut is doing all the work.
func newTieFixture() (memberIDs []string, vectorIndex map[string][]byte) {
	vectorIndex = map[string][]byte{"m0": knnVector()}
	memberIDs = []string{"m0"}
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("p%d", i)
		vectorIndex[id] = knnVector(i - 1)
		memberIDs = append(memberIDs, id)
	}
	return memberIDs, vectorIndex
}

// renderKNN renders a candidate set into a stable, diffable string.
func renderKNN(cands []densifyCandidate) string {
	var sb strings.Builder
	for _, c := range cands {
		fmt.Fprintf(&sb, "%s|%s|%.8f\n", c.A, c.B, c.Score)
	}
	return sb.String()
}

// tieGolden is the candidate set the PRE-SWAP implementation (build the full
// above-threshold candidate list per member, sort it by score descending with a
// lexicographically-smaller-partner-ID tie-break, cut to k) selected over
// newTieFixture at k=2, threshold 0.99. Captured verbatim from a run against that
// implementation.
//
// IT IS AN EXTERNAL EXPECTATION: nothing in the post-swap code derives it, and
// every line of it is a tie-break decision. m0's five-way tie yields p1 and p2 —
// the two lexicographically smallest — and NOT p4/p5, which is what a selector
// whose ties resolve by arrival, by heap position, or by anything else would
// produce.
const tieGolden = `m0|p1|0.99609375
m0|p2|0.99609375
m0|p3|0.99609375
m0|p4|0.99609375
m0|p5|0.99609375
p1|p2|0.99218750
p1|p3|0.99218750
p1|p4|0.99218750
p1|p5|0.99218750
`

// TestSelectTopicKNN_TieBreakIsUnchanged pins the tie-break.
func TestSelectTopicKNN_TieBreakIsUnchanged(t *testing.T) {
	memberIDs, vectorIndex := newTieFixture()

	got := selectTopicKNN(memberIDs, vectorIndex, 2, 0.99)
	require.Equal(t, tieGolden, renderKNN(got),
		"the tie-break changed: with more equally-scored candidates than slots, the surviving "+
			"neighbors must still be the lexicographically smallest partner IDs")

	t.Run("the_cut_actually_bites", func(t *testing.T) {
		// THE KNOWN POSITIVE for the golden above. Without a k that is smaller than
		// the tied candidate set, every candidate survives and the golden pins no
		// tie-break at all — it would be satisfied by a selector with no cut in it.
		all := selectTopicKNN(memberIDs, vectorIndex, 5, 0.99)
		require.Greater(t, len(all), len(got),
			"at k=5 the same fixture must yield MORE pairs than at k=2, or the k=2 golden is not "+
				"recording a cut and the tie-break it claims to pin is never exercised")
	})

	t.Run("selection_is_independent_of_input_order", func(t *testing.T) {
		// The tie-break's equivalence to the bounded selector's arrival-order rule
		// rests on candidates being offered in ascending ID order, which
		// selectTopicKNN obtains by sorting its members. This arm holds that
		// contract from the outside: the caller's slice order must not matter.
		shuffled := []string{"p4", "m0", "p2", "p5", "p1", "p3"}
		require.ElementsMatch(t, memberIDs, shuffled, "fixture control: same members, different order")
		require.Equal(t, tieGolden, renderKNN(selectTopicKNN(shuffled, vectorIndex, 2, 0.99)),
			"a reordered member slice must select the identical pairs — the selection sorts its "+
				"members precisely so the tie-break cannot depend on caller order")
	})
}

// newDenseFixture builds a topic of `members` co-members whose pairwise
// similarity is high enough that EVERY pair clears the threshold. That is the
// regime the ticket names — "when the threshold admits most pairs" — and the only
// one in which the per-member candidate list grows with the membership.
func newDenseFixture(members int) (memberIDs []string, vectorIndex map[string][]byte) {
	vectorIndex = make(map[string][]byte, members)
	memberIDs = make([]string, 0, members)
	for i := range members {
		id := fmt.Sprintf("t%05d", i)
		// One set bit, cycling through the 256 positions: any two members differ in
		// at most two bits, so every pair scores at least 1-2/256.
		vectorIndex[id] = knnVector(i % 256)
		memberIDs = append(memberIDs, id)
	}
	return memberIDs, vectorIndex
}

// knnSelectionAllocBytes reports the bytes ONE selectTopicKNN call allocates over
// an all-pairs-admitted topic of the given size, averaged over a measured window.
// The warmup keeps one-time growth out of the window, as the eviction-scaling gate
// in the server's store package does.
func knnSelectionAllocBytes(t *testing.T, members, k int) (bytesPerCall uint64, pairs int) {
	t.Helper()

	const warmupCalls = 2
	const measuredCalls = 4

	memberIDs, vectorIndex := newDenseFixture(members)

	for range warmupCalls {
		pairs = len(selectTopicKNN(memberIDs, vectorIndex, k, 0.9))
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	for range measuredCalls {
		pairs = len(selectTopicKNN(memberIDs, vectorIndex, k, 0.9))
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	return (after.TotalAlloc - before.TotalAlloc) / measuredCalls, pairs
}

// TestSelectTopicKNN_AllocationScalesWithSelectedPairsNotComparedPairs is the
// cost gate. It fails against a selection that materializes and sorts every
// above-threshold candidate per member and passes against a bounded one.
//
// THREE ASSERTIONS, EACH KILLING A DIFFERENT WRONG OUTCOME:
//
//   - THE KNOWN POSITIVE. Allocation at the base size must be non-zero, or a
//     fixture whose threshold admitted nothing and a genuinely bounded selection
//     read the same and the two assertions below pass on 0 vs 0.
//   - THE RATIO. Doubling the membership doubles the pairs SELECTED (members * k)
//     and quadruples the pairs COMPARED (members squared). The materializing shape
//     therefore predicts ~4.0 and a bounded one ~2.0, so the 3.0 bound sits
//     between the two predictions rather than beside either.
//   - THE EXTERNAL EXPECTATION. A ratio alone is satisfied by a selection that is
//     uniformly wasteful at both sizes, so the large size is also held under a
//     ceiling derived from the SELECTED pair count — a number that comes from the
//     request (members, k) and the map entry each selected pair costs, not from
//     the selection under test.
func TestSelectTopicKNN_AllocationScalesWithSelectedPairsNotComparedPairs(t *testing.T) {
	const k = 4
	const baseMembers = 600
	const scaledMembers = 2 * baseMembers

	// A selected pair costs a canonical key string, a map slot and a candidate
	// struct — low hundreds of bytes. 768 bytes per selected pair is far above
	// that and far below what materializing every compared pair costs: the
	// materializing shape spends ~24 bytes per compared candidate across
	// members*(members-1) comparisons, which at 1200 members is ~34MB against this
	// ceiling's ~3.7MB.
	const perSelectedPairCeiling = 768

	baseBytes, basePairs := knnSelectionAllocBytes(t, baseMembers, k)
	scaledBytes, scaledPairs := knnSelectionAllocBytes(t, scaledMembers, k)

	// FIXTURE CONTROL: the threshold must genuinely admit far more candidates than
	// k retains, or there is no cut and nothing here measures a bounded selection.
	require.Positive(t, basePairs, "fixture control: the selection must produce pairs")
	require.Less(t, scaledPairs, scaledMembers*scaledMembers/2,
		"fixture control: the selection must retain far fewer pairs than it compares")

	t.Logf("selectTopicKNN allocation: %d members -> %d B/call (%d pairs); %d members -> %d B/call (%d pairs)",
		baseMembers, baseBytes, basePairs, scaledMembers, scaledBytes, scaledPairs)

	t.Run("instrument_is_live", func(t *testing.T) {
		assert.Positive(t, baseBytes,
			"a call that allocates nothing means the fixture admitted no candidates; without this "+
				"control the assertions below pass on 0 vs 0")
	})

	t.Run("allocation_does_not_scale_with_compared_pairs", func(t *testing.T) {
		assert.Less(t, scaledBytes, 3*baseBytes,
			"doubling the membership took allocation from %d B to %d B per call — the selection is "+
				"materializing every compared candidate, not just the ones it keeps",
			baseBytes, scaledBytes)
	})

	t.Run("allocation_is_bounded_by_the_selected_pairs", func(t *testing.T) {
		ceiling := uint64(perSelectedPairCeiling * scaledPairs)
		assert.Less(t, scaledBytes, ceiling,
			"selecting %d pairs must cost under %d B, not %d B", scaledPairs, ceiling, scaledBytes)
	})
}
