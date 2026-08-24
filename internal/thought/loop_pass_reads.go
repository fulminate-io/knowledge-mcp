// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"maps"
	"slices"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// loop_pass_reads.go holds the per-pass full-corpus read memo and the
// snapshot-first residual hydrate the rewired reads share. Everything the memo
// adds lives here so none of the capped files it serves has to grow.

// passReads is the PER-UNIT-OF-WORK full-corpus read memo — one propagation pass,
// or one on-demand reflect handler call (see NewReadMemo). That work reads the same
// full-corpus sets from several stages; passReads makes the FIRST reader pay and
// every later reader in the same unit free, without changing what any of them sees.
//
// THE EDGE READS ARE UNIFIED. Four stages once issued four full-corpus edge reads
// that pivoted on the IDENTICAL id set and differed only by type filter — the
// clustering adjacency, session membership, the thought->charges map, and the
// cited-code chain. They are now ONE read over unifiedPivotEdgeTypes (memoPivotEdges),
// with each narrower consumer served a type subset of it (memoTypedEdges) and doing
// its own type filtering. Reads that pivot on a DIFFERENT set — the tension universe's
// charge-pivot reads and its four-type edge read — are deliberately NOT folded in;
// see memoTypedEdges for the superset rule that keeps that honest.
//
// It IS a CorpusSource: it delegates CorpusSnapshot / ChargeSnapshot /
// SessionSnapshot to the underlying loop, so it can be passed anywhere a
// src CorpusSource already goes and every existing type-assert against a richer
// interface keeps working — fetchTensionChargeSet (wire_tensions.go) asserts
// ChargeCorpusSource off src, and a memo that failed to forward it would silently
// re-drain the entire charge type-browse.
//
// MEMOIZED VALUES ARE SHARED, NEVER COPIED. Every consumer receives the SAME
// slice/map the first reader built, so NO CONSUMER MAY MUTATE what it gets back.
// This is not a style preference: runClusterDetection already stores the
// adjacency it received into p.lastAdj (storeDetectionResults, loop_detection.go)
// and the NEXT tick set-diffs against it (ComputeEdgeChanges), so the aliasing is
// load-bearing — a consumer that sorted or appended in place would corrupt the
// following tick's incremental baseline. Copy before mutating if a future
// consumer must.
//
// THE SUBSET RULE, stated once and relied on by every id-keyed accessor: a memo
// entry is built over an ID SET; a later request whose ids are a SUBSET of that
// set is served from the memo, and any other request falls through to an uncached
// read WITHOUT overwriting the memo. This is EXACT, not approximate. For charges:
// fetchChargesFor's per-thought value depends only on that thought's outgoing
// EdgeChargedBy edges and the hydrate of the charge ids they name, so projecting a
// superset map down to a subset of thought keys equals a fresh call with the
// subset. For adjacency: the memo stores the UNPROJECTED (nodeIDs, adj) pair for
// scope="all" and every subset request re-runs projectAdjacencySubset, which is
// literally the last thing fetchAdjacency does today.
//
// FAILURE IS NOT MEMOIZED. An accessor whose underlying read fails stores nothing
// and sets no built flag, so a later consumer in the same pass RETRIES the read
// exactly as it would today rather than inheriting a failure it cannot see.
//
// The mutex is uncontended (a pass runs on one goroutine) and is present because
// every other cross-stage field on the loop is mutex-guarded.
type passReads struct {
	src CorpusSource // the resident corpus seam (the loop); may be nil.

	mu sync.Mutex

	// adjacency: the UNPROJECTED scope="all" pair (see THE SUBSET RULE).
	adjBuilt   bool
	adjNodeIDs []string
	adj        map[string][]string

	// the ONE unified edge read. Carries every type in unifiedPivotEdgeTypes, so the
	// narrower typed reads are served from it. IT CARRIES NO ID SET — the one
	// EXCEPTION to THE SUBSET RULE above, because the entry is built from a MATCH-ALL
	// banded read, so it covers every id set by construction. The TYPE dimension is
	// unaffected and memoTypedEdges still checks it.
	pivotEdgesBuilt bool
	pivotEdges      []knowledgev1.Edge

	// the thought-pivot charge map + the thought-id set it covers.
	chargesBuilt bool
	chargeIDs    map[string]bool
	charges      map[string][]*knowledgev1.Node

	// corpus-typed node hydrates + the id set they cover.
	nodesBuilt bool
	nodeIDs    map[string]bool
	nodes      map[string]*knowledgev1.Node
}

// newPassReads builds a memo delegating to src (the loop, or nil for a degraded /
// unit-test pass). The memo's LIFETIME is ONE UNIT OF WORK — one runPass call, or
// one on-demand handler call — and it is a local, never stored on the loop, so it
// can never serve another unit of work's snapshot and there is nothing to invalidate.
func newPassReads(src CorpusSource) *passReads { return &passReads{src: src} }

// NewReadMemo builds a read memo over src for ONE unit of work: one propagation
// pass, or one on-demand handler call. The on-demand reflect handlers use it to
// compose the corpus and the charge map ONCE per call instead of once per consumer,
// so the several stages of a single handler read one PINNED snapshot and are
// guaranteed to agree with each other rather than merely tending to.
//
// It returns the concrete *passReads boxed in the interface, which is what makes the
// memo effective rather than merely present: the memoized accessors type-assert
// src.(*passReads), and a value boxed in an interface keeps its concrete type.
//
// A nil src is fine and still useful: the memo reports cold and every consumer
// drains the wire exactly as it does today, but the compositions are still deduped.
func NewReadMemo(src CorpusSource) CorpusSource { return newPassReads(src) }

// CorpusSnapshot forwards to the underlying source so the memo satisfies
// CorpusSource. A nil source is cold (warm=false) and the caller drains, exactly
// as it does with a nil src today.
func (r *passReads) CorpusSnapshot() ([]*knowledgev1.Node, bool) {
	if r == nil || r.src == nil {
		return nil, false
	}
	return r.src.CorpusSnapshot()
}

// ChargeSnapshot forwards to the underlying source when it implements
// ChargeCorpusSource, so a type-assert off the memo (fetchTensionChargeSet)
// reaches the resident charge set instead of re-draining the charge type-browse.
// A nil source, or one without the richer interface, reports cold.
func (r *passReads) ChargeSnapshot() ([]*knowledgev1.Node, bool) {
	if r == nil {
		return nil, false
	}
	cs, ok := r.src.(ChargeCorpusSource)
	if !ok {
		return nil, false
	}
	return cs.ChargeSnapshot()
}

// SessionSnapshot forwards to the underlying source when it implements
// SessionCorpusSource, so the session-label hydrate reaches the resident session
// set. A nil source, or one without the richer interface, reports cold.
func (r *passReads) SessionSnapshot() ([]*knowledgev1.Node, bool) {
	if r == nil {
		return nil, false
	}
	ss, ok := r.src.(SessionCorpusSource)
	if !ok {
		return nil, false
	}
	return ss.SessionSnapshot()
}

// memoAdjacencyAll serves the scope="all" adjacency pair once per pass. On a miss,
// or when src is not a memo, it runs fetchAdjacencyAllUncached — the SAME
// implementation the non-memo path takes, so there is exactly one adjacency
// composition in the tree. The memo is handed its own self as the source of the
// uncached build so the reads nested inside it (the thought-node set and the
// session-membership edges) are memoized too.
//
// The returned slice and map are the memo's own: DO NOT MUTATE (see passReads).
func memoAdjacencyAll(ctx context.Context, gc Caller, src CorpusSource) ([]string, map[string][]string, error) {
	r, ok := src.(*passReads)
	if !ok || r == nil {
		return fetchAdjacencyAllUncached(ctx, gc, src)
	}
	r.mu.Lock()
	if r.adjBuilt {
		nodeIDs, adj := r.adjNodeIDs, r.adj
		r.mu.Unlock()
		return nodeIDs, adj, nil
	}
	r.mu.Unlock()

	nodeIDs, adj, err := fetchAdjacencyAllUncached(ctx, gc, r)
	if err != nil {
		return nil, nil, err // FAILURE IS NOT MEMOIZED.
	}
	r.mu.Lock()
	r.adjNodeIDs, r.adj, r.adjBuilt = nodeIDs, adj, true
	r.mu.Unlock()
	return nodeIDs, adj, nil
}

// unifiedPivotEdgeTypes is the edge-type set of the ONE unified pivot-edge read:
// the five thought-cluster types plus session membership plus the thought-pivot
// charge edge. Four full-corpus reads used to pivot on the identical thought-id set
// and differ only by type filter; this set is their union, so one read serves them
// all and each consumer filters the wider result for what it wants.
//
// BUILT FROM adjacencyEdgeTypes, never re-spelling the five, so a change to the
// clustering edge set cannot silently desynchronise the two. adjacencyEdgeTypes is
// itself pinned by a landed criterion asserting it still carries both temporal types.
//
// THE DOUBLE append IS THE NON-ALIASING FORM AND IS DELIBERATE. A bare
// append(adjacencyEdgeTypes, ...) is safe only incidentally — today that slice is a
// 5-element composite literal with len==cap, so append is forced to allocate. Add a
// sixth clustering type with any spare capacity and the bare form would write THROUGH
// into adjacencyEdgeTypes' backing array and corrupt the clustering edge set at init
// time. Copy first, unconditionally.
var unifiedPivotEdgeTypes = append(append([]kgtypes.EdgeType(nil), adjacencyEdgeTypes...),
	kgtypes.EdgeKGContains, kgtypes.EdgeChargedBy)

// memoPivotEdges serves the ONE unified full-corpus edge read per pass. It is
// modeled on memoKGContainsEdges' shape with one difference: it RETURNS its error
// rather than swallowing it, because its caller — the adjacency composition — already
// propagates edge-read errors.
//
// THE UNDERLYING READ IS MATCH-ALL, NOT PIVOTED. nodeIDs derive the from_id band
// boundaries rather than selecting rows, so the result is a strict SUPERSET and every
// consumer must apply its own id-membership test (buildAdjacencyFromEdges and
// deriveSessionSiblings both do, each pinned by a criterion).
//
// FAILURE IS NOT MEMOIZED: on error nothing is stored and no flag is set, so the next
// consumer in the pass retries the read.
//
// The returned slice is the memo's own: DO NOT MUTATE.
func memoPivotEdges(ctx context.Context, gc Caller, nodeIDs []string, src CorpusSource) ([]knowledgev1.Edge, error) {
	r, _ := src.(*passReads)
	if r != nil {
		r.mu.Lock()
		// NO ID-COVERAGE TEST: the entry is a MATCH-ALL read, so it covers every id
		// set by construction. DELETED, not left in place returning true.
		if r.pivotEdgesBuilt {
			edges := r.pivotEdges
			r.mu.Unlock()
			return edges, nil
		}
		r.mu.Unlock()
	}
	edges, err := fetchAllEdgesBanded(ctx, gc, nodeIDs, unifiedPivotEdgeTypes)
	if err != nil {
		return nil, err // FAILURE IS NOT MEMOIZED.
	}
	if r != nil {
		r.mu.Lock()
		if !r.pivotEdgesBuilt {
			r.pivotEdges, r.pivotEdgesBuilt = edges, true
		}
		r.mu.Unlock()
	}
	return edges, nil
}

// memoTypedEdges serves a type SUBSET of the unified edge read. It serves from the
// memo only when TWO conditions hold: src is a *passReads, and unifiedPivotEdgeTypes
// is a SUPERSET of wantTypes. Otherwise it falls through to the exact narrow read it
// replaces, WITHOUT overwriting the memo.
//
// THERE WERE THREE. The id-coverage condition was removed when the unified read
// became match-all — it could only ever be true. The FALLTHROUGH below is
// deliberately NOT converted: it is reached exactly by the narrow, uncovered reads
// that must not become whole-graph reads.
//
// IT RETURNS THE MEMOIZED SLICE UNFILTERED, WHICH IS A REQUIREMENT ON CALLERS, NOT AN
// OBSERVATION: every caller must filter the result by edge type itself. That holds
// today — deriveSessionSiblings skips any type that is not kg-contains,
// fetchChargesUncached skips anything that is not charged-by from a requested thought,
// and codeRefProxiesByThought skips anything that is not relates-to with Method
// "code-ref" — and a future caller that does not filter must not use this accessor.
//
// THE TYPE-SUPERSET CHECK IS A REAL CHECK. Every consumer wired today asks only for
// types the unified read carries, so an omitted check would pass every other gate in
// this package while handing a future caller a silently short answer.
//
// The returned slice is the memo's own: DO NOT MUTATE.
func memoTypedEdges(ctx context.Context, gc Caller, ids []string, wantTypes []kgtypes.EdgeType, src CorpusSource) ([]knowledgev1.Edge, error) {
	r, _ := src.(*passReads)
	if r != nil && edgeTypesCovered(wantTypes) {
		r.mu.Lock()
		// No id-coverage test (see memoPivotEdges). The TYPE-superset check above is
		// untouched — the ID dimension became vacuous, the TYPE dimension did not.
		if r.pivotEdgesBuilt {
			edges := r.pivotEdges
			r.mu.Unlock()
			return edges, nil
		}
		r.mu.Unlock()
	}
	return fetchEdgesForNodeSet(ctx, gc, ids, wantTypes)
}

// edgeTypesCovered reports whether unifiedPivotEdgeTypes carries every requested
// type — the superset predicate memoTypedEdges gates on. An EMPTY wantTypes means
// "every type" at the wire, which the 7-type unified read cannot satisfy, so it is
// NOT covered.
func edgeTypesCovered(wantTypes []kgtypes.EdgeType) bool {
	if len(wantTypes) == 0 {
		return false
	}
	for _, want := range wantTypes {
		found := slices.Contains(unifiedPivotEdgeTypes, want)
		if !found {
			return false
		}
	}
	return true
}

// memoKGContainsEdges serves the single bulk EdgeKGContains read that the
// session-sibling expansion (deriveSessionSiblings) and the session-label map
// (FetchSessionLabelsByThought) group differently, so the edges are read once per
// pass and grouped twice. It is now a thin wrapper over memoTypedEdges — the
// kg-contains edges ride the unified pivot read — and exists to keep its two
// consumers' error contract in one place.
//
// FAILURE IS NOT MEMOIZED — stated here specifically because this accessor's
// underlying read swallows its error: the read failing makes deriveSessionSiblings
// return an empty sibling map and FetchSessionLabelsByThought an empty label map.
// Setting a built flag on that path would hand every later consumer in the pass an
// empty result it cannot distinguish from a genuinely session-less corpus; instead
// nothing is stored and the next consumer RETRIES the read exactly as it does today.
//
// The returned slice is the memo's own: DO NOT MUTATE.
func memoKGContainsEdges(ctx context.Context, gc Caller, thoughtIDs []string, src CorpusSource) []knowledgev1.Edge {
	edges, err := memoTypedEdges(ctx, gc, thoughtIDs, []kgtypes.EdgeType{kgtypes.EdgeKGContains}, src)
	if err != nil {
		return nil // FAILURE IS NOT MEMOIZED — the next consumer retries the read.
	}
	return edges
}

// memoCharges serves the thought-pivot charge map once per pass. A request whose
// thought ids are covered by the memoized set is served by projecting that map down
// to the requested keys (THE SUBSET RULE); anything else falls through to
// fetchChargesUncached — the exact read it replaces — without overwriting the memo.
//
// The per-thought charge slices are the memo's own: DO NOT MUTATE.
func memoCharges(ctx context.Context, gc Caller, thoughtIDs []string, src CorpusSource) map[string][]*knowledgev1.Node {
	r, _ := src.(*passReads)
	if r != nil {
		r.mu.Lock()
		if r.chargesBuilt && idSetCovers(r.chargeIDs, thoughtIDs) {
			charges := r.charges
			r.mu.Unlock()
			return projectChargeMap(charges, thoughtIDs)
		}
		r.mu.Unlock()
	}
	out, err := fetchChargesUncached(ctx, gc, thoughtIDs, src)
	if err != nil {
		return out // FAILURE IS NOT MEMOIZED — the next consumer retries the read.
	}
	if r != nil {
		r.mu.Lock()
		if !r.chargesBuilt {
			r.chargeIDs, r.charges, r.chargesBuilt = newIDSet(thoughtIDs), out, true
		}
		r.mu.Unlock()
	}
	return out
}

// memoCorpusNodes is the snapshot-first residual hydrate every corpus-typed node
// read routes through: fill what the resident snapshots already hold (thoughts,
// charges and sessions — exactly corpusNodeTypes), then issue ONE fetchNodesByIDs
// for the RESIDUAL ids only, skipping the wire call entirely when the residual is
// empty.
//
// THE RESIDUAL LEG IS WHAT KEEPS THIS EXACT RATHER THAN MERELY CHEAP. An id created
// after the cache's pinned horizon, or one of a NON-CORPUS type — a NodeFinding or
// NodeResearch charge parent (tensionClaimTypes, wire_tensions.go), a linkage proxy
// — is still hydrated from the wire exactly as today, so no consumer can lose a node
// by switching to the cache. A memo that simply DROPPED uncovered ids would make
// every charged FINDING silently vanish from the tension universe.
//
// A SUPERSET RETURN IS DELIBERATE AND SAFE: the returned map may be wider than the
// requested ids, because a covered request is served the memoized map whole. Every
// consumer indexes it BY ID ONLY (clusterIDAccessor, currentPropagatedAccessor,
// classifyBlindSpots), so do NOT add a len() over it or range it as if it were the
// requested set.
//
// The returned map is the memo's own: DO NOT MUTATE.
func memoCorpusNodes(ctx context.Context, gc Caller, ids []string, src CorpusSource) map[string]*knowledgev1.Node {
	r, _ := src.(*passReads)
	if r != nil {
		r.mu.Lock()
		if r.nodesBuilt && idSetCovers(r.nodeIDs, ids) {
			nodes := r.nodes
			r.mu.Unlock()
			return nodes
		}
		r.mu.Unlock()
	}
	out, err := hydrateCorpusNodes(ctx, gc, ids, src)
	if err != nil {
		return out // FAILURE IS NOT MEMOIZED — the next consumer retries the read.
	}
	if r != nil {
		r.mu.Lock()
		if !r.nodesBuilt {
			r.nodeIDs, r.nodes, r.nodesBuilt = newIDSet(ids), out, true
		}
		r.mu.Unlock()
	}
	return out
}

// hydrateCorpusNodes is memoCorpusNodes' uncached leg: the snapshot-first fill plus
// the ONE residual wire hydrate. It is the generalisation of the two idioms already
// in the package — fetchTensionChargeSet's type-assert-and-fall-back off src, and
// fetchNodeMap's map build straight out of the snapshot. A nil/cold source leaves
// every id in the residual, which is exactly today's behavior (one bulk hydrate of
// the whole set).
func hydrateCorpusNodes(ctx context.Context, gc Caller, ids []string, src CorpusSource) (map[string]*knowledgev1.Node, error) {
	out := make(map[string]*knowledgev1.Node, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	resident := residentCorpusByID(src)
	residual := make([]string, 0, len(ids))
	for _, id := range ids {
		if n, ok := resident[id]; ok {
			out[id] = n
			continue
		}
		residual = append(residual, id)
	}
	if len(residual) == 0 {
		return out, nil // fully resident — no wire call at all.
	}
	hydrated, err := fetchNodesByIDsErr(ctx, gc, residual)
	if err != nil {
		return out, err
	}
	maps.Copy(out, hydrated)
	return out, nil
}

// residentCorpusByID projects whichever of the three resident snapshots the source
// offers — thoughts, charges and sessions, exactly the corpusNodeTypes the cache
// drains — into one id-keyed map. A snapshot that reports cold contributes nothing,
// leaving its ids in the residual for the wire hydrate. Zero wire calls: every
// snapshot is a projection of data the pass already holds.
func residentCorpusByID(src CorpusSource) map[string]*knowledgev1.Node {
	out := map[string]*knowledgev1.Node{}
	if src == nil {
		return out
	}
	if nodes, warm := src.CorpusSnapshot(); warm {
		addNodesByID(out, nodes)
	}
	if cs, ok := src.(ChargeCorpusSource); ok {
		if nodes, warm := cs.ChargeSnapshot(); warm {
			addNodesByID(out, nodes)
		}
	}
	if ss, ok := src.(SessionCorpusSource); ok {
		if nodes, warm := ss.SessionSnapshot(); warm {
			addNodesByID(out, nodes)
		}
	}
	return out
}

// addNodesByID indexes a snapshot slice into an id-keyed map, skipping the empty id.
func addNodesByID(out map[string]*knowledgev1.Node, nodes []*knowledgev1.Node) {
	for _, n := range nodes {
		if id := n.GetId(); id != "" {
			out[id] = n
		}
	}
}

// newIDSet builds the membership set a memo entry records itself as covering.
func newIDSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// idSetCovers reports whether every requested id is inside the set a memo entry was
// built over — the predicate behind THE SUBSET RULE.
func idSetCovers(set map[string]bool, ids []string) bool {
	for _, id := range ids {
		if !set[id] {
			return false
		}
	}
	return true
}

// projectChargeMap narrows a memoized charge map to the requested thought keys,
// preserving fetchChargesFor's contract that a thought with no hydratable charge is
// ABSENT from the map rather than present-and-empty.
func projectChargeMap(charges map[string][]*knowledgev1.Node, thoughtIDs []string) map[string][]*knowledgev1.Node {
	out := make(map[string][]*knowledgev1.Node, len(thoughtIDs))
	for _, id := range thoughtIDs {
		if c, ok := charges[id]; ok {
			out[id] = c
		}
	}
	return out
}
