// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_rebuild_absorb.go — what a reset does about the segments a CONCURRENT
// publisher landed while the reset was building its replacement layer.
//
// THE WINDOW IS A PROPERTY OF ReplaceLayer'S DESIGN, NOT A BUG IN IT. BuildLayer
// captures the removal set at build time and the CAS applies that same captured set,
// so a segment published after the snapshot is not named for removal and is carried
// through — which is exactly right, because a swap that removed a set it had never
// read would silently drop a concurrent publisher's work. The consequence is that the
// survivor and the reset's own copy of the same ids are BOTH resident afterwards,
// duplicated until organic traffic happens to rebuild their partitions. This file
// consolidates them immediately instead. layer_swap.go is not edited.

// absorbBuildWindowSurvivors consolidates the segments that survived a reset's layer
// swap because a concurrent group swap published them inside the build window.
//
// THE SURVIVOR SET IS DERIVED, NOT TRACKED. After the CAS the resident set is exactly
// (published ∪ survivors), and ReplaceLayer already returns everything this swap
// published — so the delta is the survivor set and no new bookkeeping is kept on the
// manager. Deriving it also means it cannot drift out of date the way a recorded set
// could.
//
// IT IS GENERIC OVER distManager FOR THE REASON finalizeResetLayer IS: the two live
// instantiations carry different type arguments, and a per-format copy of a sequence
// whose ORDERING is its safety property is exactly where the two would drift apart.
//
// THE CRASH WINDOW IS THE PRE-CHANGE STATE. A process dying between the CAS and this
// consolidation leaves survivors resident as duplicates, consolidated by the next
// drain that touches their partitions — which is precisely today's behaviour, so no
// new failure mode appears. Nothing is deleted before its replacement is published.
func absorbBuildWindowSurvivors[Q, S any](
	dm *distManager[Q, S], published []searchengine.SegmentID,
) (survivors []searchengine.SegmentID, err error) {
	publishedSet := make(map[searchengine.SegmentID]bool, len(published))
	for _, id := range published {
		publishedSet[id] = true
	}
	for _, id := range dm.engine.ResidentSegmentIDs() {
		if !publishedSet[id] {
			survivors = append(survivors, id)
		}
	}
	// THE NO-RACE PATH IS EVERY RESET THAT DID NOT RACE A PUBLISHER, and it must cost
	// one id-only snapshot walk and nothing else.
	if len(survivors) == 0 {
		return nil, nil
	}

	// DISTINCT, NOT SUMMED, for the reason the backlog drain states at its own call: a
	// summed resident count counts a duplicated id once per segment holding it, and
	// deriving a partition count from that inflated reading manufactures a crossing the
	// real corpus never made. On THIS path the inflation is guaranteed rather than
	// possible — duplicates are the very condition being repaired.
	corpusDocs := dm.engine.DistinctResidentDocCount()

	// ONE CALL DOES BOTH HALVES. Passing the survivors as priorityLast seeds every
	// partition they span as dirty — which is the only work this call has, since it
	// supplies no documents and no supersessions — AND appends them at the end of the
	// merge union, so the CONCURRENTLY-PUBLISHED copy of a doubly-held id wins in every
	// partition it spans. The absorb derives no spans and no ordering of its own:
	// splitting them across two operands is how the seed and the priority would come to
	// disagree about which segments they mean.
	if _, _, err := replaceBucketGroups(dm, nil, nil, nil, corpusDocs, survivors); err != nil {
		return nil, err
	}
	// SWAP THEN PERSIST, which is the DRAIN's order and not the reset's. The reset
	// builds, writes and then swaps because a whole replacement layer is known before it
	// is published; a consolidation's output is not known until the swap has run, so
	// that order cannot apply here.
	wrote, err := dm.persistResident()
	if err != nil {
		return nil, err
	}
	slog.Info("segmentdist: reset absorbed build-window survivors",
		"graph", dm.target.GetGraph(), "name", dm.target.GetName(), "repo", dm.target.GetRepo(),
		"format", dm.format, "survivors", len(survivors), "corpus_docs", corpusDocs, "blobs_written", wrote)
	return survivors, nil
}
