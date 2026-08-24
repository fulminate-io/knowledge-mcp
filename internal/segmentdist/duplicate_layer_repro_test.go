// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// duplicate_layer_repro_test.go reproduces the Step 5 index loss on a corpus whose
// ids live in MORE THAN ONE segment — the state two rebuilds at different times
// leave behind, and the state the live incident was in.
//
// DISTINCT CONTENT PER DOCUMENT IS MANDATORY HERE, and it is not a style choice.
// An earlier version of this fixture gave every document the same content terms,
// which made the search probe non-discriminating: the probe id was absent from the
// top 5 BEFORE the drain as well, so a probe miss proved nothing. Every document
// therefore carries its own tok%05d token, and the probe queries that token. Any
// assertion added here must be about MEMBERSHIP COUNTS or a discriminating probe,
// never about search rank over uniform content.

// dupLayerCorpus is the distinct-id count the fixture builds. It is a CONSTANT and
// every expectation below is stated against it — never against a count read back
// from the engine, because the resident count is the quantity the defect corrupts.
const dupLayerCorpus = 8192

// dupLayerWindow is how many already-resident documents the drain re-writes.
const dupLayerWindow = 50

// dupLayerDocs builds the corpus with a per-document content token and a
// salt-dependent vector, so two layers carry the SAME ids with DIFFERENT bytes —
// different content hashes, identical membership, which is what two rebuilds at
// different times produce.
// THE VECTOR MUST BE UNIQUE PER DOCUMENT, for the same reason the content term
// must be. A purely arithmetic fill modulo 251 repeats every 251 documents, so a
// corpus this size contains dozens of documents with byte-identical vectors; the
// probe's exact-match query then ties with all of them and can fall out of the top
// k on its own merits. Encoding the index into the leading bytes makes every
// document's vector distinct, while the salted tail still separates the two layers.
func dupLayerDocs(n int, salt byte) []searchengine.Document {
	docs := make([]searchengine.Document, n)
	for i := range docs {
		vec := make([]byte, 32)
		binary.BigEndian.PutUint32(vec[0:4], uint32(i))
		for b := 4; b < len(vec); b++ {
			vec[b] = byte((i*31 + b*7 + int(salt)*17) % 251)
		}
		id := fmt.Sprintf("doc-%05d", i)
		docs[i] = searchengine.Document{
			ID:     id,
			Vector: vec,
			// The per-document token is what makes the probe discriminating.
			Fields: map[string]string{searchengine.FieldContent: dupLayerToken(i) + " alpha " + id},
		}
	}
	return docs
}

// dupLayerToken is the per-document content term the probe queries.
func dupLayerToken(i int) string { return fmt.Sprintf("tok%05d", i) }

// dupLayerName is the graph every layered-fixture caller uses. It is FIXED rather
// than per-test because the fixture's blobs are built against it once; a caller
// passing a different name would search an empty graph.
const dupLayerName = "repro"

// dupLayerSegments is how many segments the layered corpus seals into: one
// 8-aligned layer per salt, neither sharing a content hash with the other. It is
// DERIVED from the layout count, never hard-coded, so a drift in the sizing rule
// fails the builder's own guard instead of silently re-shaping the fixture.
var dupLayerSegments = 2 * searchengine.BucketCountFor(dupLayerCorpus)

// twoLayerBlobs seals the layered corpus ONCE for the whole package and hands
// every caller the same in-memory segments to import.
//
// The two 8192-document vector-index builds are the entire cost of this fixture
// and are what is shared; decoding the sealed bytes back into an engine is
// milliseconds. Sharing them is state-equivalent because a segment is a closed
// artifact: Import decodes each blob into freshly-allocated nodes, ids and
// vectors, so an importing engine holds the same layered membership the builder
// did — the summed-vs-distinct DISAGREEMENT this fixture exists to create
// included.
//
// ONLY THE SEALED BYTES CROSS TEST BOUNDARIES, matching the package's other
// shared corpus. Every caller still gets its own server, manager, counters and
// cache directory, because sharing a LIVE harness would make callers
// order-dependent — one caller's ship moves another's denominator. The blobs are
// treated as IMMUTABLE: decode reads them and copies out, so concurrent importers
// never contend.
//
// The builder outlives every test, so nothing else will clean up after it; it
// owns its cache directories and removes them on the way out. That is safe
// because Export returns in-memory blobs and the on-disk files are never read
// again.
var twoLayerBlobs = sync.OnceValue(func() []searchengine.SegmentBlob {
	ctx := context.Background()
	gt := kgtypes.GraphCode

	build := func(salt byte) *distManager[[]byte, struct{}] {
		dir, err := os.MkdirTemp("", "segmentdist-twolayer-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(dir)

		src := newSharedServerFake().viewFor(&knowledgev1.GraphSelector{}, "")
		mgr := NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(src))
		// Closed here rather than registered with a test, because this builder runs
		// under a package-level sync.OnceValue and has no test to attach to. Close
		// retires the engines' background mergers ONLY — the returned distManager's
		// engine is still Imported from and Exported below, and neither reads the
		// stop channel Close closes.
		defer mgr.Close()
		if err := mgr.ReplaceBucket(ctx, gt, dupLayerName, nil, dupLayerDocs(dupLayerCorpus, salt)); err != nil {
			panic(err)
		}
		return mgr.managerFor(gt, dupLayerName)
	}

	// Second layer: same ids, different bytes, built by a SEPARATE manager and
	// imported, so its segments carry distinct content hashes.
	dmA := build(0)
	if err := dmA.engine.Import(build(99).engine.Export(), nil); err != nil {
		panic(err)
	}

	blobs := dmA.engine.Export()
	if len(blobs) != dupLayerSegments {
		panic(fmt.Sprintf("layered corpus must seal into %d segments, got %d", dupLayerSegments, len(blobs)))
	}
	return blobs
})

// twoLayerFixture gives one caller the layered corpus: an engine holding TWO
// layers over the same id set, each layer 8-aligned, so every segment spans two
// partitions under the count the drain derives. It returns the manager and its
// HNSW distManager plus the base documents.
//
// The documents are rebuilt per caller rather than shared. They are microseconds
// to generate, and handing the same slice to several callers' write paths would
// share mutable state for no saving.
func twoLayerFixture(t *testing.T) (*Manager, *distManager[[]byte, struct{}], []searchengine.Document) {
	t.Helper()
	gt := kgtypes.GraphCode

	require.Equal(t, 8, searchengine.BucketCountFor(dupLayerCorpus), "layout count")

	_, gc := newSegmentHarness(t)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	dm := mgr.managerFor(gt, dupLayerName)
	require.NoError(t, dm.engine.Import(twoLayerBlobs(), nil))

	return mgr, dm, dupLayerDocs(dupLayerCorpus, 0)
}

// presentMemberIDs reports which of the supplied ids still resolve to a resident
// segment. It is the DUPLICATE-PROOF form of the corpus size, and the reason it is
// needed is itself part of the defect: ResidentDocCount sums each segment's
// DocCount, and DocCount counts a duplicated id once per copy, so on a layered
// corpus it reads high while the distinct membership is unchanged.
//
// WHAT IT PROVES, stated precisely: VectorByID resolves through the route map, so
// this is a PRESENCE probe over the resident set, not a liveness probe — an id
// killed in place but still routed would count as present. That makes it a
// CONSERVATIVE measure of loss: every id it reports missing is genuinely gone from
// the resident set, so a shortfall here cannot be an artifact of the probe.
func presentMemberIDs(dm *distManager[[]byte, struct{}], ids []searchengine.Document) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, d := range ids {
		if _, ok := dm.engine.VectorByID(d.ID); ok {
			out[d.ID] = true
		}
	}
	return out
}

// TestDuplicateIdMergeKeepsEveryDistinctMember is THE LOSS GATE.
//
// Repartitioning a corpus whose ids appear in more than one segment must keep
// every DISTINCT member. The expectation is the fixture's own constant, never a
// number read back from the engine.
//
// RED TODAY, by an unmissable margin: the drain returns roughly half the corpus.
func TestDuplicateIdMergeKeepsEveryDistinctMember(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "repro"
	mgr, dm, base := twoLayerFixture(t)

	residentBefore := dm.engine.ResidentDocCount()
	t.Logf("LAYERED resident=%d (distinct corpus=%d) segments=%d derivedCount=%d",
		residentBefore, dupLayerCorpus, len(dm.engine.Export()), searchengine.BucketCountFor(residentBefore))

	// A probe OUTSIDE the drain window, so nothing about the write path explains a
	// miss. With per-document content this is discriminating: it is found before.
	probeIdx := dupLayerCorpus - 1
	probe := base[probeIdx]
	hitsBefore, err := mgr.Search(ctx, gt, name, dupLayerToken(probeIdx), probe.Vector, 5)
	require.NoError(t, err)
	require.True(t, hitsContain(hitsBefore, probe.ID),
		"the probe must be findable BEFORE the drain, or a miss afterwards proves nothing")

	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, base[:dupLayerWindow]))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	live := presentMemberIDs(dm, base)
	t.Logf("AFTER resident=%d distinctMembersPresent=%d segments=%d",
		dm.engine.ResidentDocCount(), len(live), len(dm.engine.Export()))

	require.Len(t, live, dupLayerCorpus,
		"every distinct member must survive the repartition — a shared constituent must not be consumed by whichever partition swaps first")
	require.Equal(t, dupLayerCorpus, dm.engine.ResidentDocCount(),
		"the repartitioned corpus holds each id exactly once: resident equals the distinct-id count, not the layered count and not half of it")

	hitsAfter, err := mgr.Search(ctx, gt, name, dupLayerToken(probeIdx), probe.Vector, 5)
	require.NoError(t, err)
	require.True(t, hitsContain(hitsAfter, probe.ID),
		"a document outside the drain window must still be searchable after it")
}

// TestRepartitionConvergesToOnePerPartition is THE SURPLUS GATE.
//
// IT MUST RUN THE CONCURRENT PATH. Manager.ReEmitDirtyBuckets is what fans out
// across partitions; driving the swaps serially satisfies this today and would
// make the gate vacuous. Do not "simplify" it into a serial loop.
//
// RED TODAY: 30 segments over 16 partitions.
func TestRepartitionConvergesToOnePerPartition(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "repro"
	mgr, dm, base := twoLayerFixture(t)

	// The partition count must be the one the DRAIN derives, and the drain reads the
	// resident count AFTER the write path has sealed the window (drainFormat passes
	// dm.engine.ResidentDocCount()). Every term here is a fixture CONSTANT — two
	// layers over the distinct corpus, plus the sealed window — so the expectation is
	// never a number read back from the run it is judging.
	//
	// That this count is derived from a duplicate-inflated resident total rather than
	// from the true distinct corpus is the COUNT-SOURCE TRAP, and it is tracked
	// separately. It is not this gate's subject: whatever partition count is in
	// force, the resident set must hold at most one segment per partition.
	residentAtDrain := 2*dupLayerCorpus + dupLayerWindow
	partitions := searchengine.BucketCountFor(residentAtDrain)

	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, base[:dupLayerWindow]))
	// The CONCURRENT drain — the fan-out across partitions is the point.
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	segments := len(dm.engine.Export())
	t.Logf("AFTER segments=%d partitions=%d", segments, partitions)
	require.LessOrEqual(t, segments, partitions,
		"a repartition converges to at most one segment per partition; surplus survivors mean partitions sharing a constituent each published beside the others")
}

// hitsContain reports whether any hit carries the id.
func hitsContain(hits []searchengine.Hit, id string) bool {
	for _, h := range hits {
		if h.ID == id {
			return true
		}
	}
	return false
}
