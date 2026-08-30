// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// Environment levers for the residue inventory. The test is INERT unless the pool
// directory is named, so it costs a skip in an ordinary run and reports a real pool
// when an operator points it at one.
const (
	// residuePoolDirEnv names a directory of stored .seg blobs — an HNSW L2 pool, or
	// a copy of one. Point it at a COPY: this loads every blob and the engine holds
	// the mappings for the life of the test.
	residuePoolDirEnv = "RESIDUE_POOL_DIR"
	// residuePoolExpectEnv is the duplication figure an operator observed OUTSIDE
	// this process — the daemon's shipped-minus-live at the moment the pool was
	// snapshotted. It is the one expectation this test cannot derive from the bytes
	// it is reading, which is exactly why it is supplied rather than computed: a
	// check whose expected value comes from the thing under test proves nothing.
	residuePoolExpectEnv = "RESIDUE_POOL_EXPECT_DUPLICATION"
	// residueOverlapRows bounds how many segment-pair rows the report prints. The
	// totals above them are complete; this caps the render, not the measurement.
	residueOverlapRows = 40
)

// TestResiduePoolComposition is a PERMANENT DIAGNOSTIC INSTRUMENT, not a regression
// test: pointed at a stored HNSW segment pool it reports, from the bytes alone, what
// that pool's duplication actually consists of.
//
// IT LOADS THE POOL THE WAY A COLD START DOES, and that is the whole reason it can
// be believed. The cache is the production diskSegmentCache with the production read
// advice; the engine is built with the same merge-disarmed options managerFor uses,
// so no background consolidation moves the corpus underneath the measurement; and
// the load itself is loadResidentFromL2 — the real path, which enumerates the
// directory, maps every hit, and imports the batch so the stored blobs' own
// supersession records decide what is published. A bespoke walk would measure a
// directory; this measures the resident set a restart would actually serve.
//
// TOMBSTONES ARE NIL HERE, and the report says so where it matters. A live daemon
// seeds every import from its owner's tombstone set, so its live count excludes
// documents deleted since the blobs were written. This process has no owner, so its
// live count is the distinct resident count and nothing more — the two figures are
// the same measurement here and are NOT the same measurement in the daemon.
//
// THE RECONCILE IS BETWEEN TWO INDEPENDENT INSTRUMENTS. The engine's duplication is
// its per-segment DocCount sum minus the length of its route map; this test's is a
// multiset built from its own decode of each blob's membership. They share no
// intermediate, so agreement is evidence, and a probe that silently read nothing
// fails the sum check rather than reporting a clean zero.
func TestResiduePoolComposition(t *testing.T) {
	raw := os.Getenv(residuePoolDirEnv)
	if raw == "" {
		t.Skipf("%s is unset; this diagnostic runs only when pointed at a stored segment pool", residuePoolDirEnv)
	}
	// CLEANED AND ANCHORED AT THE BOUNDARY, once, before the value reaches anything
	// that opens a file. An operator-supplied directory is the one untrusted input
	// this test has, and every path below is built under it — so the traversal
	// segments come out here rather than at each of the several sinks downstream.
	dir, err := filepath.Abs(filepath.Clean(raw))
	require.NoError(t, err, "%s must resolve to an absolute path", residuePoolDirEnv)

	cache := newDiskSegmentCache(dir, 0, hnswReadAdvice)
	keys := cache.Keys()
	require.NotEmpty(t, keys, "%s indexes no .seg files", dir)

	engine := closeOnCleanup(t, searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
	}))
	// The graph name is DERIVED from the directory it was handed. It reaches the
	// load's log lines only, and naming a graph the caller did not point at would put
	// a wrong label on every line this run emits.
	dm := newDistManager(engine, cache, graphSelector(kgtypes.GraphCode, filepath.Base(dir)), hnsw.New().Name())
	require.NoError(t, dm.loadResidentFromL2())

	segs := readResidueInventory(t, dir, cache, keys)
	resident := make(map[searchengine.SegmentID]bool, len(keys))
	for _, id := range engine.ResidentSegmentIDs() {
		resident[id] = true
	}
	for _, s := range segs {
		s.published = resident[s.id]
	}

	shipped, distinct := engine.ResidentDocCount(), engine.DistinctResidentDocCount()
	require.Positive(t, distinct, "the load published no documents, so every figure below would be a vacuous zero")
	bucketCount := searchengine.BucketCountFor(distinct)
	spans := engine.SegmentSpans(bucketCount)
	for _, s := range segs {
		s.spans = spans[s.id]
	}
	overlap := computeOverlap(segs)

	reportResidueInventory(t, dir, segs, bucketCount)
	reportResidueCounts(t, engine, shipped, distinct, len(keys), overlap)
	reportResidueOverlap(t, segs, overlap)
	reportResidueCohorts(t, segs)
	reportResidueRecords(t, segs)

	// RECONCILE. The first check is the anti-vacuity guard: if the decode above had
	// returned empty membership for every blob the multiset would report a clean zero
	// duplication, which is indistinguishable from a converged pool. Summing this
	// test's own per-segment counts against the engine's stamped total makes that case
	// a failure instead.
	sum := 0
	for _, s := range segs {
		if s.published {
			sum += s.distinct
		}
	}
	require.Equal(t, shipped, sum,
		"this test's per-segment distinct counts do not sum to the engine's shipped total")
	require.Equal(t, shipped-distinct, overlap.instances,
		"the engine's shipped-minus-distinct disagrees with the duplicate multiset built from the same blobs")

	if want := os.Getenv(residuePoolExpectEnv); want != "" {
		n, err := strconv.Atoi(want)
		require.NoError(t, err, "%s must be an integer", residuePoolExpectEnv)
		require.Equal(t, n, overlap.instances,
			"the pool's duplication differs from the figure observed outside this process; "+
				"a difference is drift between the snapshot and the observation, not a decode fault")
	}
}

// reportResidueInventory prints the per-segment table: what each stored file is, what
// it holds, what it records, and where it sits in the partition layout.
//
// IT REPORTS THE MTIME SPAN AND CLASSIFIES THROUGH NEITHER. An mtime is the time
// the file in THIS directory was written, which for a pool copied without -p is the
// time of the copy — 84 files a second apart is a cp, not a corpus history, and a
// cohort split derived from that would be an invention. The span is printed so a
// reader can see which they are looking at; the classification below is structural,
// derived from the partitions a segment spans and from how many of its documents no
// other segment holds.
func reportResidueInventory(t *testing.T, dir string, segs []*residueSegment, bucketCount int) {
	t.Helper()
	first, last := segs[0].modTime, segs[0].modTime
	for _, s := range segs {
		if s.modTime.Before(first) {
			first = s.modTime
		}
		if s.modTime.After(last) {
			last = s.modTime
		}
	}
	t.Logf("RESIDUE POOL %s", dir)
	t.Logf("  files=%d  layout=%d partitions", len(segs), bucketCount)
	t.Logf("  mtime span %s .. %s (%s) — this is when each file was written HERE, so a copied pool "+
		"reports the copy and no cohort below is derived from age",
		first.Format("2006-01-02T15:04:05.000"), last.Format("2006-01-02T15:04:05.000"), last.Sub(first))
	t.Logf("  %-16s %10s  %-19s %8s %8s %6s %6s  %s", "segment", "bytes", "mtime", "members", "distinct", "parts", "sole", "record")
	for _, s := range segs {
		record := "-"
		if len(s.superseded) > 0 || len(s.cohort) > 0 {
			record = "superseded=" + strconv.Itoa(len(s.superseded)) + " cohort=" + strconv.Itoa(len(s.cohort))
		}
		if !s.published {
			record += " DECLINED-AT-IMPORT"
		}
		t.Logf("  %-16s %10d  %-19s %8d %8d %6d %6d  %s",
			s.id[:16], s.fileBytes, s.modTime.Format("2006-01-02T15:04:05"),
			len(s.members), s.distinct, len(s.spans), s.sole, record)
	}
}

// reportResidueCounts prints what the engine says about the set it published, beside
// the duplication this test derived independently.
func reportResidueCounts(
	t *testing.T, engine *searchengine.SegmentedIndex[[]byte, struct{}],
	shipped, distinct, files int, overlap residueOverlap,
) {
	t.Helper()
	published := len(engine.ResidentSegmentIDs())
	t.Logf("COLD LOAD (diskSegmentCache.Keys -> GetMapped -> engine.Import, via loadResidentFromL2)")
	t.Logf("  stored files=%d  published segments=%d  declined at import=%d", files, published, files-published)
	t.Logf("  shipped (sum of per-segment DocCount)=%d", shipped)
	t.Logf("  distinct resident documents=%d", distinct)
	t.Logf("  live resident documents=%d  (no tombstone seed in this process, so live == distinct here; "+
		"a daemon's live figure excludes documents deleted since the blobs were written)", engine.LiveResidentCount())
	t.Logf("  duplication (shipped - distinct)=%d", shipped-distinct)
	t.Logf("DUPLICATE MULTISET (built from this test's own decode of each blob)")
	t.Logf("  documents resident in more than one segment=%d", overlap.docs)
	t.Logf("  redundant copies (sum of occurrences-1)=%d", overlap.instances)
	occs := make([]int, 0, len(overlap.histogram))
	for n := range overlap.histogram {
		occs = append(occs, n)
	}
	sort.Ints(occs)
	for _, n := range occs {
		t.Logf("    resident in %d segments: %d documents", n, overlap.histogram[n])
	}
}

// reportResidueOverlap prints which segments share documents with which, and which
// segments hold nothing of their own.
//
// A SEGMENT WHOSE SOLE COUNT IS ZERO IS ENTIRELY REDUNDANT: every document it holds
// has another resident copy, so retiring it would remove no document from the
// searchable set. That is the concrete form of "resident but superseded" — stated
// from membership rather than from a record, which is the only way to state it for a
// pool whose blobs carry no records at all.
func reportResidueOverlap(t *testing.T, segs []*residueSegment, overlap residueOverlap) {
	t.Helper()
	type row struct {
		pair   [2]searchengine.SegmentID
		shared int
	}
	rows := make([]row, 0, len(overlap.pairs))
	for pair, n := range overlap.pairs {
		rows = append(rows, row{pair: pair, shared: n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].shared != rows[j].shared {
			return rows[i].shared > rows[j].shared
		}
		return rows[i].pair[0] < rows[j].pair[0]
	})
	t.Logf("SEGMENT OVERLAPS (%d overlapping pairs; showing up to %d)", len(rows), residueOverlapRows)
	for i, r := range rows {
		if i == residueOverlapRows {
			break
		}
		t.Logf("  %s <-> %s  shares %d documents", r.pair[0][:16], r.pair[1][:16], r.shared)
	}

	var redundant, unique []*residueSegment
	for _, s := range segs {
		if !s.published {
			continue
		}
		if s.sole == 0 {
			redundant = append(redundant, s)
			continue
		}
		unique = append(unique, s)
	}
	t.Logf("CLASSIFICATION BY MEMBERSHIP")
	t.Logf("  segments holding at least one document no other segment holds: %d", len(unique))
	t.Logf("  segments every document of which has another resident copy:     %d", len(redundant))
	for _, s := range redundant {
		t.Logf("    fully-redundant %s  distinct=%d  spans partitions %v", s.id[:16], s.distinct, s.spans)
	}
}

// reportResidueRecords answers the one bit that decides whether a pool's residue is
// the pre-record class: do its blobs carry supersession records AT ALL, and do those
// records name blobs that are still stored beside them?
//
// A RECORD NAMING A STORED SIBLING IS THE HEALTHY CASE — the import declines that
// sibling and the duplication never reaches the searchable set. Records absent
// entirely means the blobs were written by a binary that had no record to write, and
// nothing on disk can tell a later reader which copy the swap replaced.
func reportResidueRecords(t *testing.T, segs []*residueSegment) {
	t.Helper()
	stored := make(map[searchengine.SegmentID]bool, len(segs))
	for _, s := range segs {
		stored[s.id] = true
	}
	withRecord, namingStored, namedIDs := 0, 0, 0
	for _, s := range segs {
		if len(s.superseded) == 0 {
			continue
		}
		withRecord++
		namedIDs += len(s.superseded)
		for _, id := range s.superseded {
			if stored[id] {
				namingStored++
			}
		}
	}
	t.Logf("SUPERSESSION RECORDS")
	t.Logf("  blobs carrying a record=%d of %d", withRecord, len(segs))
	t.Logf("  segment ids those records name as superseded=%d, of which still stored here=%d", namedIDs, namingStored)
	if withRecord == 0 {
		t.Logf("  NO BLOB IN THIS POOL RECORDS A SUPERSESSION. Every stored file therefore stands on its own " +
			"at import, and any duplication between them is invisible to the load — the state that exists " +
			"when the blobs were published by a binary predating the durable record.")
	}
}
