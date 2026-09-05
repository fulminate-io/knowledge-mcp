// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// residueSegment is one stored .seg file as this diagnostic reads it: the file
// facts, the membership the format decodes out of it, and what the engine did with
// it once the production cold path had imported the whole directory.
type residueSegment struct {
	id        searchengine.SegmentID
	fileBytes int64
	modTime   time.Time
	// members is what the format indexes, live or dead, in the format's own order.
	// distinct is that set deduplicated, which is the number the engine stamps as
	// the segment's DocCount at entryFromDecoded.
	members  []searchengine.ExternalID
	distinct int
	// superseded and cohort are the blob's own supersession record: what the swap
	// that published it replaced, and every id that swap published. Both nil for a
	// record-less blob, which is what every blob written before the record existed
	// is, and what an ordinary non-consolidating seal still is.
	superseded []searchengine.SegmentID
	cohort     []searchengine.SegmentID
	// published reports whether the engine is actually serving this segment after
	// the import — false means Import DECLINED it on another blob's record.
	published bool
	// spans is the set of partitions this segment holds members of, derived by
	// walking membership under the layout's bucket count (never by arithmetic — a
	// segment several counts behind spans more partitions than a sibling formula
	// would find). sole is how many of its members no OTHER segment in the pool
	// holds: a sole of zero is a segment whose every document has another resident
	// copy.
	spans []int
	sole  int
}

// readResidueInventory reads every key the L2 cache indexes and decodes it the way
// the import path does: the supersession envelope comes off first, and what is left
// is what the format is handed. Nothing here re-derives the envelope layout — the
// split is searchengine's own, reached through SegmentPayload.
//
// IT READS THE HEAP COPY (cache.Get) rather than mapping a second time. The load
// under measurement already mapped every one of these files and handed the mappings
// to the engine, whose entries own their release; taking a second mapping here
// would meter a lifetime this diagnostic does not control.
func readResidueInventory(
	t *testing.T, dir string, cache *diskSegmentCache, keys []searchengine.SegmentID,
) []*residueSegment {
	t.Helper()
	format := hnsw.Format{}
	out := make([]*residueSegment, 0, len(keys))
	for _, id := range keys {
		blob, ok := cache.Get(id)
		require.True(t, ok, "cache key %s does not resolve to a readable file", id)

		info, err := os.Stat(filepath.Join(dir, id+".seg"))
		require.NoError(t, err)

		superseded, cohort, err := searchengine.SupersededBy(blob)
		require.NoError(t, err, "segment %s carries a damaged supersession envelope", id)
		payload, err := searchengine.SegmentPayload(blob)
		require.NoError(t, err)
		seg, err := format.Decode(payload)
		require.NoError(t, err, "segment %s does not decode as %s", id, format.Name())

		members := seg.IDs()
		out = append(out, &residueSegment{
			id:         id,
			fileBytes:  info.Size(),
			modTime:    info.ModTime(),
			members:    members,
			distinct:   len(distinctIDs(members)),
			superseded: superseded,
			cohort:     cohort,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].modTime.Equal(out[j].modTime) {
			return out[i].modTime.Before(out[j].modTime)
		}
		return out[i].id < out[j].id
	})
	return out
}

// TestSegmentPayloadStripsAnEnvelopeTheFormatWouldRefuse pins the exported split the
// inventory above is built on, from the position that needed it: another package,
// holding stored bytes, with no access to the envelope's layout.
//
// THE DISCRIMINATING PAIR IS THE LAST TWO ASSERTIONS. A split that returned its input
// unchanged would satisfy every other line here, and would go on producing correct
// answers for a pool whose blobs happen to carry no records — which is most pools.
// Asserting that the payload decodes AND that the whole blob does not is what makes
// this a test of the split rather than of the format.
func TestSegmentPayloadStripsAnEnvelopeTheFormatWouldRefuse(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	const name = "payloadSplit"
	// A LAYER, THEN A GROUP SWAP OVER IT. The layer swap deliberately stamps NO
	// record (layer_swap.go: "NO DURABLE SUPERSESSION RECORD IS STAMPED HERE"), so it
	// only supplies the resident constituents; the write-driven re-emit that follows
	// retires some of them and IS what stamps its outputs.
	stageRebuildRun(t, ctx, mgr, kgtypes.GraphCode, name, vecContentDocs(1024))
	first, err := mgr.FinalizeRebuild(ctx, kgtypes.GraphCode, name)
	require.NoError(t, err)
	require.True(t, first.Swapped, "the layer must land, or the re-emit below supersedes nothing")
	require.NoError(t, mgr.ReplaceBucket(ctx, kgtypes.GraphCode, name, nil, vecContentDocsSeed(64, 1024)))

	format := hnsw.Format{}
	enveloped := 0
	for _, blob := range mgr.managerFor(kgtypes.GraphCode, name).engine.Export() {
		// THE SUBJECT IS STORED BYTES, so the stored form is reassembled here. An
		// exported blob arrives ALREADY SPLIT — the record in Envelope, the payload
		// in Bytes — and running the split over its Bytes would be splitting a
		// payload that by construction carries no envelope: every assertion below
		// would compare a slice against itself and pass while testing nothing. What
		// this test exists to pin is the exported split as a reader OFF DISK sees
		// it, which is envelope followed by payload in one slice.
		stored := append(append([]byte{}, blob.Envelope...), blob.Bytes...)

		superseded, _, supErr := searchengine.SupersededBy(stored)
		require.NoError(t, supErr)
		payload, payErr := searchengine.SegmentPayload(stored)
		require.NoError(t, payErr)
		if len(superseded) == 0 {
			require.Empty(t, blob.Envelope, "a record-less blob must carry no envelope")
			require.Len(t, payload, len(stored), "a record-less blob must pass through unchanged")
			continue
		}
		enveloped++
		require.Less(t, len(payload), len(stored), "an enveloped blob's payload must be shorter than the blob")
		_, decodePayload := format.Decode(payload)
		require.NoError(t, decodePayload, "the payload beneath the envelope must decode")
		_, decodeWhole := format.Decode(stored)
		require.Error(t, decodeWhole,
			"the whole enveloped blob decoded, so the format is not refusing an envelope and this split "+
				"would be indistinguishable from a passthrough")
	}
	require.Positive(t, enveloped,
		"no exported blob carried a supersession record, so the split under test was never exercised")
}

// distinctIDs deduplicates a segment's membership. A blob built before the engine
// counted distinct members can carry the same external id twice, so this is the set
// every count below is taken over.
func distinctIDs(ids []searchengine.ExternalID) map[searchengine.ExternalID]struct{} {
	out := make(map[searchengine.ExternalID]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

// residueOverlap is the duplication this pool carries, computed from the per-segment
// ID sets alone — deliberately independent of the engine's own counters, so the two
// numbers agreeing is evidence rather than a restatement.
type residueOverlap struct {
	// docs is how many distinct external ids appear in more than one segment;
	// instances is the number of REDUNDANT copies (sum of occurrences-1), which is
	// what a shipped-minus-live duplication figure reports.
	docs      int
	instances int
	// histogram maps an occurrence count to how many documents carry it: 2 -> n
	// means n documents are resident in exactly two segments.
	histogram map[int]int
	// pairs maps a segment pair to how many documents the two share, keyed by the
	// pair's sorted ids so an overlap is counted once.
	pairs map[[2]searchengine.SegmentID]int
}

// computeOverlap builds the duplicate multiset over the segments the engine is
// actually SERVING. A declined blob is on disk but out of the searchable set, so
// counting its documents would report duplication no reader can observe.
func computeOverlap(segs []*residueSegment) residueOverlap {
	holders := make(map[searchengine.ExternalID][]searchengine.SegmentID)
	for _, s := range segs {
		if !s.published {
			continue
		}
		for id := range distinctIDs(s.members) {
			holders[id] = append(holders[id], s.id)
		}
	}
	out := residueOverlap{
		histogram: make(map[int]int),
		pairs:     make(map[[2]searchengine.SegmentID]int),
	}
	soleOf := make(map[searchengine.SegmentID]int, len(segs))
	for _, owners := range holders {
		out.histogram[len(owners)]++
		if len(owners) == 1 {
			soleOf[owners[0]]++
			continue
		}
		out.docs++
		out.instances += len(owners) - 1
		sort.Strings(owners)
		for i := range owners {
			for j := i + 1; j < len(owners); j++ {
				out.pairs[[2]searchengine.SegmentID{owners[i], owners[j]}]++
			}
		}
	}
	for _, s := range segs {
		s.sole = soleOf[s.id]
	}
	return out
}
