// SPDX-License-Identifier: Apache-2.0

package tools

// manage_status_coverage_segcell.go — the segment-coverage CELL's readers: the
// probe the coverage table calls per row, and the optional text-pool seam it
// consults for a graph with no vector engine.
//
// SPLIT OUT OF manage_status_coverage.go for the 500-line cap that file sits
// against. The split is by SUBJECT rather than by size: everything here answers
// "what does this graph's segment cell say", and nothing else in the table does.

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// segCoveredFor reads the SERVER-shipped HNSW-segment-covered doc count AND the
// LIVE in-memory engine resident doc count for a row's graph via the nil-safe
// SegmentCoverage seam. Segments exist for every graph kgtypes.HasRebuildableSegments
// admits, which is every builtin except linkage — read that predicate rather
// than a list here, because a list of families is what rots when one is added or
// retired. It is the SAME gate buildHealFactory and the manual rebuild_segments
// op use, so the status column reports coverage for exactly the graph set the
// auto-heal arm services. Reporting coverage for a raw graph is what makes
// manage(status) answerable for one, which is how an operator confirms a
// collected document is searchable. A graph with no rebuildable segments
// returns (0, 0, false) and the column renders "—". When the seam is unwired
// (degraded headless mode) or the shipped probe errs, it also returns (0, 0, false)
// — a placeholder, not a hard failure of the status table. The live resident read is
// a single snapshot walk (no RPC and no load); it is surfaced so a live-pool
// collapse (live 0 while covered is N) is detectable instead of masked behind the
// shipped figure.
//
// IT IS ONLY EVER CALLED FOR A GRAPH IN THE WORKING SET, and that fence is the
// CALLER'S (collectSegProbes). It has to be, because BOTH reads below interact:
// ShippedSegmentDocCount routes to Manager.LoadResidentDocCount and imports the
// graph's whole L2 pool, and LiveResidentDocCount reaches Manager.managerFor, which
// lazily constructs the per-graph engine and its cache directory. Neither is
// permitted for a graph no direct interaction has admitted, so the gate cannot live
// inside this function's existing fences — poolEvictedFor and the type/wiring
// checks are about whether a probe would be MEANINGFUL, not about whether it is
// ALLOWED.
//
// IT READS THE TEXT POOL FOR A GRAPH WITH NO VECTOR ENGINE, and `embedded` is the
// only reason that parameter exists. Both readers below count the HNSW engine, so
// on a KEYLESS install — no embed credential, therefore no vectors and no vector
// engine — this cell read `shipped 0 · live 0` however many documents the BM25
// pool held and however well text search served them. That is a false negative on
// the operator's own instrument for "is search working".
//
// THE DISCRIMINATOR IS THE EMBEDDED COUNT, NOT A ZERO FROM THE HNSW READ, and the
// distinction is what keeps a keyed graph's cell byte-identical. `embedded == 0`
// means this graph HAS no vector corpus, which is a durable fact the status table
// already holds (GraphStats.BinaryVectorCount, the same denominator the coverage
// ratio compares against). Branching on "the HNSW pair came back (0,0)" instead
// would re-route a KEYED graph whose pool is merely cold or mid-load, changing
// cells this has no business changing.
func segCoveredFor(
	ctx context.Context, deps ClientDeps, gt kgtypes.GraphType, name string, embedded int,
) (covered, liveResident int, hasSeg bool) {
	if !kgtypes.HasRebuildableSegments(gt) {
		return 0, 0, false
	}
	sr := deps.SegmentCoverage()
	if sr == nil {
		return 0, 0, false
	}
	if poolEvictedFor(deps, gt, name) {
		return segCoveredForEvicted()
	}
	if embedded == 0 {
		if shipped, live, ok := bm25SegCoveredFor(ctx, deps, gt, name); ok {
			return shipped, live, true
		}
		// The capability is absent (a fixture deps, or a client with no Manager), so
		// fall through to the vector readers rather than inventing a pair: their
		// answer for such a deps is the one every existing row already renders.
	}
	c, err := sr.ShippedSegmentDocCount(ctx, gt, name)
	if err != nil {
		return 0, 0, false
	}
	return c, liveResidentFor(ctx, sr, gt, name), true
}

// liveResidentDestinationReader is the OPTIONAL capability that answers the live
// resident count FOR THE DESTINATION THIS CALL IS BOUND TO.
//
// THE PAIR THIS CELL RENDERS HAS TO COME OFF ONE ENGINE. ShippedSegmentDocCount
// above resolves the destination inside the Manager (forRequest) and answers off the
// {storage,account} child that serves this caller, while the plain
// LiveResidentDocCount takes no ctx and answers off the root. On a
// destination-bound client — the ordinary wiring, since every user call binds one —
// that pairing rendered `shipped N · live 0`, which is exactly the live-pool
// COLLAPSE signal this cell documents, fired by the reader rather than by the pool.
//
// TYPE-ASSERTED for the reason bm25TextCoverageReader and poolEvictedReader state: a
// required method on SegmentCoverageReader would have to be implemented by every
// fake that already implements SegmentCoverage(), none of which holds an engine to
// resolve a destination against. The production adapter always satisfies it and a
// compile-time proof beside that adapter pins the shape, so the decline below is
// reachable only by a fixture.
type liveResidentDestinationReader interface {
	LiveResidentDocCountFor(ctx context.Context, gt kgtypes.GraphType, name string) int
}

// liveResidentFor reads the live resident doc count for the bound destination,
// declining to the root-scoped reading for a deps without the capability — which is
// the number such a deps has always rendered.
func liveResidentFor(ctx context.Context, sr SegmentCoverageReader, gt kgtypes.GraphType, name string) int {
	if dr, ok := sr.(liveResidentDestinationReader); ok {
		return dr.LiveResidentDocCountFor(ctx, gt, name)
	}
	return sr.LiveResidentDocCount(gt, name)
}

// bm25TextCoverageReader is the OPTIONAL deps capability the vector-less cell
// reads through.
//
// TYPE-ASSERTED for the reason poolEvictedReader and loadLiveResidentReader state:
// a required method on SegmentCoverageReader would have to be implemented by every
// fake that already implements SegmentCoverage() — twenty-five of them — none of
// which holds a BM25 engine to answer from.
type bm25TextCoverageReader interface {
	BM25SegmentDocCounts(ctx context.Context, gt kgtypes.GraphType, name string) (shipped, live int, skipped bool, err error)
}

// bm25SegCoveredFor reads the text pool's (shipped, live) pair, reporting whether
// the capability was present AND answered.
//
// AN ABSENT CAPABILITY, A FAILED LOAD AND AN EVICTED POOL ALL REPORT ok=false, and
// collapsing them here is right because the caller does the same thing with each:
// it falls back to the vector readers, which are what a deps without this seam has
// always rendered. What must NOT happen is returning a fabricated (0, 0, true) —
// that would state a measurement nobody took, which is the exact failure the cell
// is being fixed for.
func bm25SegCoveredFor(
	ctx context.Context, deps ClientDeps, gt kgtypes.GraphType, name string,
) (shipped, live int, ok bool) {
	sr, isReader := deps.SegmentCoverage().(bm25TextCoverageReader)
	if !isReader {
		return 0, 0, false
	}
	s, l, skipped, err := sr.BM25SegmentDocCounts(ctx, gt, name)
	if err != nil || skipped {
		return 0, 0, false
	}
	return s, l, true
}
