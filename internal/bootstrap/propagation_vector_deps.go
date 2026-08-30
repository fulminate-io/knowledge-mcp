// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// propagation_vector_deps.go holds the two adapters that bind the propagation
// loop's package-local vector seams to the client's resident segment manager.
//
// BOTH ADAPTERS ARE LAZY AND READINESS-GATED, for two independent reasons:
//
//  1. WIRING ORDER. wireRuntimesBackground calls wirePropagationRuntime — which
//     constructs the loop AND starts it, immediately firing the boot cluster
//     detection — well before it reaches ensureSegmentManager. At propagation-wire
//     time c.segmentMgr is still NIL. An adapter that CAPTURED the manager at
//     construction would be permanently nil in production: every pass would take
//     the fallback and issue the full server drain forever, while every log line
//     and test still looked healthy.
//
//  2. THE MEMORY MODEL. Reading c.segmentMgr lazily is necessary but not
//     sufficient — the propagation goroutine would be reading a plain field that
//     the wiring goroutine writes, which is a data race whether or not it ever
//     manifests, and a -race failure. c.segmentMgr is a named beneficiary of the
//     pipelineReady publish edge, so every read below goes THROUGH PipelineReady()
//     first: the atomic Load pairs with the wiring goroutine's Store and gives this
//     goroutine a legal, ordered view of the handle.
//
// Both adapters are therefore written the same way: check PipelineReady() FIRST,
// then read c.segmentMgr. Never the reverse.
//
// Both are bound to the (knowledge, "default") graph the propagation loop reflects
// over, which is why the loop-side interfaces carry no graph type or name.

// propagationGraphName is the graph name the propagation loop reflects over,
// paired with kgtypes.GraphKnowledge at every call site below. The loop never
// reflects over any other graph, so the adapters bind the selector rather than
// threading it through the seam.
const propagationGraphName = "default"

var errSegmentManagerNotReady = errors.New("bootstrap: segment manager not wired yet — resident vector resolution unavailable")

// residentVectorAdapter implements clientthought.VectorResident over the client's
// resident segment manager: a by-id stored-vector read with ZERO RPC. It is the
// SAME Manager.VectorByID seam the mode:"similar" search claim resolves its query
// vector through, read here with the opposite (and equally legitimate) handling of
// ok=false — see the loop-side interface doc.
type residentVectorAdapter struct{ c *client }

func (a residentVectorAdapter) VectorByID(ctx context.Context, externalID string) ([]byte, bool, error) {
	if a.c == nil || !a.c.PipelineReady() {
		return nil, false, errSegmentManagerNotReady
	}
	mgr := a.c.segmentMgr
	if mgr == nil {
		return nil, false, errSegmentManagerNotReady
	}
	return mgr.VectorByID(ctx, kgtypes.GraphKnowledge, propagationGraphName, externalID)
}

// coverageGateAdapter implements clientthought.SegmentCoverageGate over the
// per-format segment observation probe. It consults the HNSW arm ONLY: that is the
// sole engine carrying vectors, so gating on any-arm degeneracy would let a
// degenerate BM25 arm veto perfectly good vector resolution.
//
// Every decline carries a reason rather than a bare false, so the fallback to the
// server drain is never silent in the leaf-attachment log line.
type coverageGateAdapter struct{ c *client }

func (a coverageGateAdapter) HNSWCoverageTrustworthy(ctx context.Context) (bool, string, error) {
	if a.c == nil || !a.c.PipelineReady() {
		return false, "segment manager not wired yet", nil
	}
	mgr := a.c.segmentMgr
	if mgr == nil {
		return false, "segment manager not wired yet", nil
	}
	obs, err := mgr.ResidentObservationsByFormat(ctx, kgtypes.GraphKnowledge, propagationGraphName)
	if err != nil {
		return false, "segment coverage probe failed", err
	}
	v, ok := armObservationFor(obs, hnsw.New().Name())
	if !ok {
		return false, "hnsw arm unmeasured", nil
	}
	if v.Err != nil {
		return false, "hnsw arm unmeasured", v.Err
	}
	// AN EVICTED ARM IS NOT A MEASURED ARM, and this branch must stay AHEAD of the
	// degeneracy test: an evicted arm reports ResidentAfterLoad 0, which
	// degenerateAgainstEmbedded reads as a lost pool — so without this branch the
	// probe would either call the pool trustworthy on a measurement nobody took, or
	// declare it degenerate and drive a rebuild that undoes the eviction. Either way
	// VectorByID would materialize the whole HNSW pool on an hourly pass, and it
	// deliberately does not stamp the search touch, so the next budget pass evicts it
	// again: reload, evict, reload, forever.
	//
	// DECLINING SENDS THIS PASS DOWN THE SERVER-DRAIN PATH for an evicted knowledge
	// graph, which costs more per pass than a resident by-id read would. That is the
	// ticket's own ruling applied rather than a new trade: a background arm must not
	// re-load an evicted pool.
	if v.Evicted {
		return false, "hnsw arm evicted — not measured", nil
	}
	// THE DENOMINATOR IS THE GRAPH'S EMBEDDED-NODE COUNT. segmentdist lost the shipped
	// count this comparison used to run against, so the verdict is computed here.
	embedded, eerr := tools.GraphEmbeddedCount(ctx, a.c.GraphCaller(), kgtypes.GraphKnowledge, propagationGraphName)
	if eerr != nil {
		return false, "embedded-count read failed", eerr
	}
	// THIS SITE KEEPS THE RATIO BAND DELIBERATELY, AND WAS NOT MISSED BY THE BAND FLIP.
	// It asks a DIFFERENT QUESTION from the heal arms: not "does this graph need a
	// rebuild" but "is the HNSW arm trustworthy enough to resolve vectors by id". A
	// TRUST question wants exactly what a band gives — a coarse "is this pool broadly
	// populated" — because resolving by id against a half-populated pool silently
	// returns misses, and it wants that answer at ANY moment rather than only at
	// pipeline quiescence, which is the one condition under which the exact verdict is
	// formed. Routing this to the exact predicate would make the trust answer
	// unavailable away from quiescence, which is when this pass actually runs.
	if degenerateAgainstEmbedded(v.ResidentAfterLoad, embedded) {
		return false, "hnsw arm degenerate", nil
	}
	return true, "hnsw arm measured and non-degenerate", nil
}
