// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// reset_absorb_survivors_test.go — a reset that races a concurrent publisher.
//
// THE RACE IS DRIVEN DETERMINISTICALLY RATHER THAN RACED. layer_swap_test.go states
// the technique at TestReplaceLayerPreservesAConcurrentlyAddedSegment and this file
// reuses it: splitting BuildLayer from ReplaceLayer makes the interval between the
// snapshot and the CAS an explicit region the test writes into. Everything here is a
// symbol that already exists; nothing about the engine is stubbed.
//
// THE TWO COPIES ARE MADE DISTINGUISHABLE BY SALT. dupLayerDocs encodes the salt into
// every vector byte from index 4 on, so the SAME id under two salts carries two
// DIFFERENT vectors — which is what lets a test ask WHICH copy survived rather than
// only whether one did.

// resetWorkFor buckets a whole corpus into the partition work a reset stages, at the
// partition count the corpus derives.
func resetWorkFor(docs []searchengine.Document) []searchengine.BucketWork {
	// count-provenance: corpus-derived — docs is the whole corpus a reset stages.
	count := searchengine.BucketCountFor(len(docs))
	byBucket := make(map[int][]searchengine.Document, count)
	for _, d := range docs {
		b := searchengine.BucketOf(d.ID, count)
		byBucket[b] = append(byBucket[b], d)
	}
	work := make([]searchengine.BucketWork, 0, len(byBucket))
	for b := range count {
		if ds, ok := byBucket[b]; ok {
			work = append(work, searchengine.BucketWork{Bucket: b, Docs: ds})
		}
	}
	return work
}

// concurrentSalt is the THIRD salt, distinct from both layers of the shared fixture.
//
// IT MUST NOT BE 99, AND THAT WAS MEASURED RATHER THAN ASSUMED. With the concurrent
// documents carrying the same salt as the fixture's second layer, a drained
// partition's merge output is byte-identical to that layer's own segment for the
// partition — same content hash, so the "new" segment IS an id the build's captured
// removal set names, and the reset swap legitimately sweeps it. Six of eight
// partitions collapsed that way, and the concurrent copies for their ids never
// survived the swap at all: there was nothing for the absorb to prefer. A third salt
// keeps every concurrent output a genuinely new segment.
const concurrentSalt = byte(7)

// The concurrent window is split so BOTH survivor shapes are represented, because
// they discriminate different defects. groupSwapHalf's copies land in DRAINED
// partitions, one survivor per partition, each spanning ONE partition. tailHalf's
// copies land in a single SEALED TAIL, which holds ids from every partition they hash
// to and therefore spans MANY — and a multi-span survivor is the only shape that can
// tell a merge priority applied at the UNION from one applied per partition.
//
// THE MULTI-SPAN TAIL IS BUILT THROUGH THE ENGINE PRIMITIVE, NOT THROUGH THE MANAGER'S
// WRITE PATH, and that is a deliberate consequence of per-partition sealing: the write
// entry points now seal one segment per partition, so they can no longer produce a
// multi-span segment and this fixture would silently lose the shape it exists to test.
// A multi-span segment remains an ORDINARY PRODUCTION STATE — bucket.go's own words are
// that a segment aligned to an older count "can therefore sit SEVERAL counts behind,
// spanning one partition per doubling it has missed" — so the copy rule still has to
// win in every partition such a segment spans. AddSealAndSupersede is the same engine
// call the write path drives; only the per-partition grouping above it is bypassed.
const (
	groupSwapHalf = dupLayerWindow / 2
	tailHalf      = dupLayerWindow - groupSwapHalf
)

// raceResetAgainstAConcurrentPublish opens a reset's build window, publishes TWO
// concurrent writers inside it, and closes the window with the CAS. It returns the ids
// ReplaceLayer reported as published and the concurrent window's documents.
//
// It stops AT THE CAS. Both tests below start from this same state and differ only in
// what they do next, which is what makes the characterization test a genuine control
// on the gate rather than a restatement of it.
func raceResetAgainstAConcurrentPublish(
	t *testing.T,
) (*distManager[[]byte, struct{}], []searchengine.Document, []searchengine.SegmentID, []searchengine.Document) {
	t.Helper()
	ctx := context.Background()
	gt := kgtypes.GraphCode

	mgr, dm, base := twoLayerFixture(t)

	// (1) THE WINDOW OPENS. BuildLayer captures its removal set here — the layers
	// resident at this instant, and nothing published afterwards.
	built, err := dm.engine.BuildLayer(resetWorkFor(base))
	require.NoError(t, err)
	require.Positive(t, built.Len(), "fixture control: the reset must have built a layer to swap in")

	// (2) TWO CONCURRENT PUBLISHERS LAND INSIDE THE WINDOW, over DISJOINT id ranges so
	// no id is claimed by both and the expected winner is unambiguous.
	concurrent := dupLayerDocs(dupLayerCorpus, concurrentSalt)[:dupLayerWindow]

	// (2a) A concurrent GROUP SWAP — the ticket's named case. Its outputs are aligned
	// to the count it drained at, so each spans one partition.
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, dupLayerName, concurrent[:groupSwapHalf]))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, dupLayerName))

	// (2b) A concurrent SEALED TAIL, left undrained. One segment holding ids from many
	// partitions, which is the multi-span survivor the copy rule has to win in EVERY
	// partition rather than only in its lowest-numbered one. Sealed through the engine
	// primitive for the reason the tailHalf comment gives.
	sealed, err := dm.engine.AddSealAndSupersede(concurrent[groupSwapHalf:])
	require.NoError(t, err)
	require.True(t, sealed.Created,
		"fixture control: the tail must be a NEW segment, or there is no survivor for the swap to carry")

	// (3) THE WINDOW CLOSES. The captured removal set does not name what step 2
	// published, so those segments are carried through and SURVIVE.
	published, _, err := dm.engine.ReplaceLayer(built)
	require.NoError(t, err)

	return dm, base, published, concurrent
}

// TestResetSwapPreservesBuildWindowSurvivorsAsDuplicates is the CHARACTERIZATION
// GUARD, green before AND after this change because it stops at the CAS.
//
// ITS JOB IS TO PROVE THE FIXTURE ACTUALLY CREATES THE RACE. Without it every
// assertion in the gate below is satisfiable by a fixture in which nothing survived
// the swap and there was never anything to absorb.
func TestResetSwapPreservesBuildWindowSurvivorsAsDuplicates(t *testing.T) {
	dm, _, published, _ := raceResetAgainstAConcurrentPublish(t)

	resident := dm.engine.ResidentSegmentIDs()
	summed, distinct := dm.engine.ResidentDocCount(), dm.engine.DistinctResidentDocCount()
	t.Logf("POST-SWAP resident_segments=%d published=%d summed_docs=%d distinct_docs=%d",
		len(resident), len(published), summed, distinct)

	require.Greater(t, len(resident), len(published),
		"the resident set must be STRICTLY LARGER than what the swap published — that difference IS the "+
			"build-window survivor set, and a fixture without one tests nothing")
	require.Greater(t, summed, distinct,
		"and those survivors hold ids the reset layer also holds, so the summed resident count reads "+
			"above the distinct membership: that is the duplicated state, observed rather than assumed")
}

// TestResetSwapAbsorbsBuildWindowSurvivors is the gate.
//
// HONEST RED-FIRST LABEL: this gate cannot be behaviourally red against the unfixed
// tree, because absorbBuildWindowSurvivors has no predecessor to assert against — its
// red is a build failure naming an undefined symbol. The behavioral evidence that the
// defect is real is the characterization test above, which observes the duplicated
// state directly.
func TestResetSwapAbsorbsBuildWindowSurvivors(t *testing.T) {
	dm, base, published, concurrent := raceResetAgainstAConcurrentPublish(t)

	// FIXTURE CONTROL FOR THE DISCRIMINATION CLAIM, asserted BEFORE the absorb consumes
	// the survivors. the_concurrently_published_copy_wins can only tell a union-level
	// merge priority from a per-partition one if some survivor spans MORE THAN ONE
	// partition — a per-partition reorder wins a single-span survivor's only partition
	// either way. The sealed tail is that survivor, and this is where that is measured
	// rather than assumed.
	preSpans := dm.engine.SegmentSpans(searchengine.BucketCountFor(dm.engine.DistinctResidentDocCount()))
	publishedSet := make(map[searchengine.SegmentID]bool, len(published))
	for _, id := range published {
		publishedSet[id] = true
	}
	multiSpan := 0
	for id, held := range preSpans {
		if !publishedSet[id] && len(held) > 1 {
			multiSpan++
		}
	}
	require.Positive(t, multiSpan,
		"fixture control: at least one survivor must span several partitions — the sealed tail holds all %d "+
			"of its ids in one segment, and without a multi-span survivor the copy-rule subtest cannot "+
			"discriminate a union-level priority from a per-partition one", tailHalf)

	survivors, err := absorbBuildWindowSurvivors(dm, published)
	require.NoError(t, err)
	require.NotEmpty(t, survivors,
		"fixture control: the absorb must have had survivors to consolidate, or every subtest below is "+
			"measuring a reset that never raced anything")

	t.Run("zero_preserved_duplicates", func(t *testing.T) {
		require.Equal(t, dm.engine.DistinctResidentDocCount(), dm.engine.ResidentDocCount(),
			"after the absorb every id is held exactly once: the summed resident count and the distinct "+
				"membership must agree")
	})

	t.Run("no_member_dropped", func(t *testing.T) {
		// Stated against the fixture CONSTANT, never against a count read back from the
		// engine — the resident count is the quantity a consolidation defect corrupts.
		require.Len(t, presentMemberIDs(dm, base), dupLayerCorpus,
			"consolidating duplicates must not drop members: all %d distinct ids must still resolve",
			dupLayerCorpus)
	})

	t.Run("at_most_one_segment_per_partition", func(t *testing.T) {
		partitions := searchengine.BucketCountFor(dm.engine.DistinctResidentDocCount())
		segments := len(dm.engine.Export())
		t.Logf("AFTER-ABSORB segments=%d partitions=%d", segments, partitions)
		require.LessOrEqual(t, segments, partitions,
			"the absorb converges the resident set to at most one segment per partition")
	})

	t.Run("the_concurrently_published_copy_wins", func(t *testing.T) {
		// THE COPY-RULE CATCHER, asserted over the WHOLE window rather than one probe.
		//
		// WHY EVERY ID. A priority applied per-BUCKET rather than at the merge UNION wins
		// only the lowest-numbered partition a survivor spans and loses every other one.
		// The window's ids hash across the fixture's partitions, so a single-probe
		// assertion can land on the partition a broken mechanism happens to win and pass
		// against it. Asserting all of them cannot.
		resetCopy := make(map[string][]byte, len(concurrent))
		for _, d := range base[:dupLayerWindow] {
			resetCopy[d.ID] = d.Vector
		}
		for _, want := range concurrent {
			// VACUITY GUARD, per id: the two salts must produce DIFFERENT bytes, or the
			// assertion below is satisfied by a fixture whose copies are identical.
			require.NotEqual(t, want.Vector, resetCopy[want.ID],
				"vacuity guard: the reset copy and the concurrent copy of %s must differ", want.ID)

			got, ok := dm.engine.VectorByID(want.ID)
			require.True(t, ok, "%s must still be resident", want.ID)
			require.Equal(t, want.Vector, got,
				"the CONCURRENTLY-PUBLISHED copy of %s must win — the surviving bytes are the reset "+
					"layer's, which means the absorb installed a stale copy over a fresher concurrent write",
				want.ID)
		}
	})
}
