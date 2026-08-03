// SPDX-License-Identifier: Apache-2.0

//go:build segmentdist_diag

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// duplicate_layer_diag_test.go is INSTRUMENTATION for the Step 5 investigation, not
// a gate. It records what the resident set is MADE OF after the drain — how many
// survivors are original constituents versus freshly published outputs — because
// the surplus and the loss make opposite predictions about that composition and
// only a measurement separates them.
//
// It asserts nothing about the defect; the locked gates in
// duplicate_layer_repro_test.go do that. Deleting this file loses evidence, not
// coverage.
//
// Because it is instrumentation rather than a gate, it is opt-in behind a build
// tag and does not run by default. To take the measurement:
// go test -tags segmentdist_diag -run TestDiag ./internal/segmentdist/

// TestDiagDuplicateLayerSurvivorComposition prints the survivor breakdown for both
// drive orders over the identical fixture.
func TestDiagDuplicateLayerSurvivorComposition(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "repro"

	t.Run("concurrent drain via ReEmitDirtyBuckets", func(t *testing.T) {
		mgr, dm, base := twoLayerFixture(t)
		originals := segmentIDSet(dm)
		partitions := searchengine.BucketCountFor(dm.engine.ResidentDocCount())

		require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, base[:dupLayerWindow]))
		require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

		reportComposition(t, dm, base, originals, partitions)
	})

	t.Run("count the swaps the concurrent fan-out actually performs", func(t *testing.T) {
		_, dm, base := twoLayerFixture(t)
		originals := segmentIDSet(dm)
		corpusDocs := dm.engine.ResidentDocCount()
		partitions := searchengine.BucketCountFor(corpusDocs)

		// Call the fan-out directly so the number of NON-EMPTY publishes is observable;
		// ReEmitDirtyBuckets reports only an error.
		published, err := replaceBucketGroups(dm, nil, base[:dupLayerWindow], nil, corpusDocs)
		require.NoError(t, err)
		t.Logf("FANOUT partitions=%d nonEmptyPublishes=%d distinctPublishedIDs=%d",
			partitions, len(published), len(uniqueIDs(published)))

		reportComposition(t, dm, base, originals, partitions)
	})

	t.Run("serial drive one partition at a time", func(t *testing.T) {
		_, dm, base := twoLayerFixture(t)
		originals := segmentIDSet(dm)
		partitions := searchengine.BucketCountFor(dm.engine.ResidentDocCount())

		// Drive the SAME swaps the concurrent path would, one partition at a time,
		// against the constituent map computed ONCE up front — which is exactly what
		// the production caller does before it fans out.
		spans := dm.engine.SegmentSpans(partitions)
		constituentsOf := map[int][]searchengine.SegmentID{}
		for sid, buckets := range spans {
			for _, b := range buckets {
				constituentsOf[b] = append(constituentsOf[b], sid)
			}
		}

		emitted, empty := 0, 0
		for b := range partitions {
			published, err := dm.engine.ReplaceBucket(b, partitions, constituentsOf[b], nil, nil)
			require.NoError(t, err)
			if published == "" {
				empty++
				t.Logf("  partition %2d published NOTHING — %d constituents named, none still resident",
					b, len(constituentsOf[b]))
				continue
			}
			emitted++
		}
		t.Logf("SERIAL partitions=%d emitted=%d publishedNothing=%d", partitions, emitted, empty)

		reportComposition(t, dm, base, originals, partitions)
	})
}

// segmentIDSet snapshots the resident segment ids.
func segmentIDSet(dm *distManager[[]byte, struct{}]) map[searchengine.SegmentID]bool {
	out := map[searchengine.SegmentID]bool{}
	for _, blob := range dm.engine.Export() {
		out[blob.ID] = true
	}
	return out
}

// reportComposition prints how the post-drain resident set breaks down.
func reportComposition(
	t *testing.T, dm *distManager[[]byte, struct{}], base []searchengine.Document,
	originals map[searchengine.SegmentID]bool, partitions int,
) {
	t.Helper()
	after := segmentIDSet(dm)
	survivingOriginals, freshOutputs := 0, 0
	for id := range after {
		if originals[id] {
			survivingOriginals++
			continue
		}
		freshOutputs++
	}
	present := presentMemberIDs(dm, base)
	t.Logf("COMPOSITION partitions=%d segmentsAfter=%d survivingOriginals=%d freshOutputs=%d consumedOriginals=%d",
		partitions, len(after), survivingOriginals, freshOutputs, len(originals)-survivingOriginals)
	t.Logf("MEMBERSHIP distinctPresent=%d of %d (missing %d) residentDocCount=%d",
		len(present), len(base), len(base)-len(present), dm.engine.ResidentDocCount())
}

// uniqueIDs dedups a published-id list.
func uniqueIDs(ids []searchengine.SegmentID) map[searchengine.SegmentID]bool {
	out := map[searchengine.SegmentID]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// TestDiagProbeRecallAfterDrain measures whether the post-drain probe miss is a
// MEMBERSHIP loss or ordinary HNSW approximation: it reports the id's presence in
// the engine, and its rank recall at several k, over a sample rather than one
// query.
func TestDiagProbeRecallAfterDrain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "repro"
	mgr, dm, base := twoLayerFixture(t)

	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, base[:dupLayerWindow]))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	present := presentMemberIDs(dm, base)
	t.Logf("MEMBERSHIP present=%d of %d segments=%d", len(present), len(base), len(dm.engine.Export()))

	probeIdx := dupLayerCorpus - 1
	probe := base[probeIdx]
	_, inEngine := dm.engine.VectorByID(probe.ID)
	t.Logf("PROBE id=%s residentInEngine=%v", probe.ID, inEngine)

	for _, k := range []int{5, 10, 50, 200} {
		hits, err := mgr.Search(ctx, gt, name, dupLayerToken(probeIdx), probe.Vector, k)
		require.NoError(t, err)
		t.Logf("  k=%3d hits=%3d found=%v", k, len(hits), hitsContain(hits, probe.ID))
	}

	// Sampled recall over 200 documents spread across the corpus, which is the shape
	// the tree's own HNSW integration test uses (a single query may legitimately miss).
	found, sampled := 0, 0
	for i := 0; i < dupLayerCorpus; i += dupLayerCorpus / 200 {
		d := base[i]
		hits, err := mgr.Search(ctx, gt, name, dupLayerToken(i), d.Vector, 10)
		require.NoError(t, err)
		sampled++
		if hitsContain(hits, d.ID) {
			found++
		}
	}
	t.Logf("SAMPLED RECALL@10 = %d/%d = %.3f", found, sampled, float64(found)/float64(sampled))
}
