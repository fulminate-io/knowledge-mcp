// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// The real-format test kit. buildManager (manager_test.go) is hardwired to the
// [mockQuery,mockStats] mock format and builds its cache internally, so it is NOT
// usable for the prune-safety tests that need (a) a REAL HNSW/BM25 engine, (b) the
// reclaimMerged OnMerge hook wired, and (c) a seam-injectable instrumented cache.
// This kit factors that ONE shared construction so the invariant / restart-tail /
// property / isolation tests reuse it instead of re-wiring var-before-assign
// OnMerge each time.

// reclaimEngineOpts builds engine Options whose merge fires readily (one segment
// per Add via MinSegmentDocs, low count target) AND carries the reclaim hook for
// the supplied distManager pointer (var-before-assign captured by the caller).
func reclaimEngineOpts[Q, S any](dmPtr **distManager[Q, S], countTarget int) searchengine.Options {
	return reclaimEngineOptsWithHookDelay(dmPtr, countTarget, 0)
}

// reclaimEngineOptsWithHookDelay is reclaimEngineOpts with a test-only stall in
// FRONT of the reclaim call, so the hook's Put lands a known interval after the
// merge published.
//
// THIS IS THE PUBLISH-TO-RECLAIM WINDOW MADE SUMMONABLE. doMerge publishes the
// consolidated segment by CAS and counts it BEFORE it calls this hook, so between
// the publish and the hook's Put there is an interval in which a live segment has
// no L2 file — clause 1 of assertLiveSetBackedByL2. In an ordinary run that
// interval is however long the merge's own Put happens to take, which is why the
// failure it produces only ever showed up as a load-dependent flake. A delay makes
// the interval a number the test chooses, so a wait that returns inside it fails
// EVERY time rather than under contention.
//
// IT WRAPS THE CALL, IT DOES NOT REPLACE IT: the reclaim that runs is the
// production reclaimMerged, unmodified. A delay of zero is exactly the undelayed
// closure, so the ordinary constructors keep their behaviour.
func reclaimEngineOptsWithHookDelay[Q, S any](
	dmPtr **distManager[Q, S], countTarget int, hookDelay time.Duration,
) searchengine.Options {
	return searchengine.Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  0.33,
		SegmentCountTarget: countTarget,
		OnMerge: func(res searchengine.MergeResult) {
			if hookDelay > 0 {
				time.Sleep(hookDelay)
			}
			(*dmPtr).reclaimMerged(res)
		},
	}
}

// buildHNSWReclaimManager wires a REAL HNSW distManager over a fresh shared server
// fake + an instrumented cache (wrapping a real *diskSegmentCache rooted at
// cacheDir), with reclaimMerged installed as OnMerge. countTarget gates the
// count-driven merge trigger (pass a high value like 1<<30 to disable it; a low
// value to force count-driven merges).
//
//nolint:unparam // gt is the intentional named API: it is half the cache-dir key this manager is rooted at, and these fixtures happen to exercise code graphs
func buildHNSWReclaimManager(
	t *testing.T, gt kgtypes.GraphType, name, cacheDir string, countTarget int,
) (*distManager[[]byte, struct{}], *instrumentedCache) {
	t.Helper()
	return buildHNSWReclaimManagerOn(t, gt, name, cacheDir, countTarget)
}

// buildHNSWReclaimManagerOn is buildHNSWReclaimManager over an EXISTING cache dir —
// the seam restart tests need to reconstruct a fresh distManager (a cold process,
// carrying none of the prior one's state) against the SAME on-disk L2.
//
// IT USED TO TAKE A SEGMENT SOURCE TOO, which the two phases shared so their reads
// landed under one target key. The shared thing is now the DIRECTORY and nothing
// else, which is the more honest fixture: a restart shares a disk, not an object.
func buildHNSWReclaimManagerOn(
	t *testing.T, gt kgtypes.GraphType, name, cacheDir string, countTarget int,
) (*distManager[[]byte, struct{}], *instrumentedCache) {
	t.Helper()
	return buildHNSWReclaimManagerWithHookDelay(t, gt, name, cacheDir, countTarget, 0)
}

// buildHNSWReclaimManagerWithHookDelay is buildHNSWReclaimManagerOn whose reclaim
// hook stalls hookDelay before it reclaims, holding the publish-to-reclaim window
// open for a known interval (see reclaimEngineOptsWithHookDelay). A zero delay is
// the ordinary manager, which is why the two undelayed constructors delegate here.
func buildHNSWReclaimManagerWithHookDelay(
	t *testing.T, gt kgtypes.GraphType, name, cacheDir string, countTarget int, hookDelay time.Duration,
) (*distManager[[]byte, struct{}], *instrumentedCache) {
	t.Helper()
	target := graphSelector(gt, name)
	ic := newInstrumentedCache(newDiskSegmentCache(cacheDir, 0, adviceRandom))

	var dm *distManager[[]byte, struct{}]
	engine := closeOnCleanup(t, searchengine.New[[]byte, struct{}](
		hnsw.New(), reclaimEngineOptsWithHookDelay(&dm, countTarget, hookDelay)))
	dm = newDistManager(engine, ic, target, hnsw.New().Name())
	return dm, ic
}

// buildBM25ReclaimManager is the BM25 counterpart to buildHNSWReclaimManager.
func buildBM25ReclaimManager(
	t *testing.T, gt kgtypes.GraphType, name, cacheDir string, countTarget int,
) (*distManager[bm25.Query, *bm25.CorpusStats], *instrumentedCache) {
	t.Helper()
	return buildBM25ReclaimManagerOn(t, gt, name, cacheDir, countTarget)
}

// buildBM25ReclaimManagerOn is the BM25 counterpart of buildHNSWReclaimManagerOn.
func buildBM25ReclaimManagerOn(
	t *testing.T, gt kgtypes.GraphType, name, cacheDir string, countTarget int,
) (*distManager[bm25.Query, *bm25.CorpusStats], *instrumentedCache) {
	t.Helper()
	target := graphSelector(gt, name)
	ic := newInstrumentedCache(newDiskSegmentCache(cacheDir, 0, adviceRandom))

	var dm *distManager[bm25.Query, *bm25.CorpusStats]
	engine := closeOnCleanup(t, searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), reclaimEngineOpts(&dm, countTarget)))
	dm = newDistManager(engine, ic, target, bm25.New().Name())
	return dm, ic
}

// bm25SearchAllLiveIDs returns the ids of every live doc the BM25 engine surfaces
// for the corpus-wide token "alpha" (every vecContentDocs doc carries it in
// FieldContent), with a k far above the corpus size so nothing is truncated. This
// is the exact full-corpus enumeration clause-3 of assertLiveSetBackedByL2 needs
// for BM25.
func bm25SearchAllLiveIDs(dm *distManager[bm25.Query, *bm25.CorpusStats], corpusSize int) func() []searchengine.ExternalID {
	return func() []searchengine.ExternalID {
		hits := dm.engine.Search(bm25.NewQuery("alpha"), corpusSize*4+16)
		ids := make([]searchengine.ExternalID, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		return ids
	}
}

// hnswRecallOK reports whether every doc in want (its own vector as the query)
// recovers ITSELF within the top-k for the bulk of the sample, and that no doc in
// absent is ever returned for its own query. HNSW is approximate, so exact
// full-corpus enumeration via one query is impossible — the honest property is
// high per-doc self-recall plus hard absence of deleted docs. Returns the recall
// fraction and whether any absent doc leaked.
func hnswRecallOK(
	dm *distManager[[]byte, struct{}], want []searchengine.Document, absent map[searchengine.ExternalID]struct{},
) (recall float64, absentLeaked bool) {
	if len(want) == 0 {
		return 1, false
	}
	recovered := 0
	for _, d := range want {
		if _, dead := absent[d.ID]; dead {
			continue
		}
		hits := dm.engine.Search(d.Vector, 10)
		for _, h := range hits {
			if _, dead := absent[h.ID]; dead {
				absentLeaked = true
			}
			if h.ID == d.ID {
				recovered++
				break
			}
		}
	}
	live := len(want) - len(absent)
	if live <= 0 {
		return 1, absentLeaked
	}
	return float64(recovered) / float64(live), absentLeaked
}

// idSetExcept builds the live-id set: every doc id in docs minus the deleted set.
func idSetExcept(docs []searchengine.Document, deleted map[searchengine.ExternalID]struct{}) map[searchengine.ExternalID]struct{} {
	out := make(map[searchengine.ExternalID]struct{}, len(docs))
	for _, d := range docs {
		if _, dead := deleted[d.ID]; dead {
			continue
		}
		out[d.ID] = struct{}{}
	}
	return out
}
