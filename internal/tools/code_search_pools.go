// SPDX-License-Identifier: Apache-2.0

// code_search_pools.go — pool selection for one code query: whether this search
// reads a single repo pool or a base pool plus its branch overlay, and how many
// candidates it asks the engine for.

package tools

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// codeSearchOverfetch is how many candidates a code query asks each pool for, as
// a multiple of the caller's limit. It exists because results are DROPPED after
// ranking: the hydrate read skips any id the node read no longer returns
// (tombstoned or deleted between rank and hydrate), and the path_prefix filter
// runs after that. Asking for exactly limit therefore returns fewer than limit
// whenever anything drops, under a banner reporting a healthy index.
//
// CEILING: the per-(repo, query) hydrate read is bounded at
// codeSearchOverfetch*limit ids instead of limit — a declared doubling, chosen
// because the drops being covered are incidental (a handful of tombstoned or
// branch-filtered nodes), not systematic.
//
// COST, both arms. The hydrate stays ONE batched ids[] read per (repo, query):
// over-fetching doubles the ids in a single request rather than adding a
// request, so no N+1 appears. The engine side is not free — the vector arm sets
// its search beam to at least k, so doubling k roughly doubles the ANN beam
// width and the per-segment work with it. At the limits this path actually runs
// (10 to 50) that is sub-millisecond to a few milliseconds per pool, which is
// why the doubling is accepted rather than tuned.
//
// HONEST LIMIT: this does NOT guarantee a full limit of results. A highly
// selective path_prefix can filter out an unbounded fraction of any candidate
// set. It removes the common silent shortfall and does not pretend to remove
// every one — and a set that is still short is visible, because the rendered
// header prints the real post-filter count.
const codeSearchOverfetch = 2

// codeSearchPoolHits resolves one query's ranked hits from the right pool shape,
// deciding that shape EXACTLY ONCE so no caller can open half of it.
//
// A branch search reads the base pool AND its overlay pool through the two-pool
// arm, which ranks the pools against each other on raw engine scores before
// fusing — UNLESS the branch's own bucket already holds the whole corpus, which
// shippedCompleteForUnifiedSearch decides and which collapses the read to that one
// pool. A default-branch search reads the single repo pool. The two-pool arm
// itself decides what a failing pool means: a base-pool failure fails the search,
// an overlay-pool failure degrades to the base pool.
//
// THE COLLAPSED ARM READS THE BRANCH POOL. Once the gate is satisfied the branch's
// bucket is the complete corpus and base is the stale one, so reading base there
// would serve every branch query from the default branch's index — a wrong answer
// that a test counting pools cannot see. Nothing about the surviving arms changes:
// the union remains the gate's safe direction for an incomplete bucket, and the
// degrade record on the missing-arm and failure paths stays exactly as written.
func codeSearchPoolHits(
	ctx context.Context,
	cdeps codeSearchDeps,
	base, branch, query string,
	queryVec []byte,
	k int,
) []searchengine.Hit {
	overlay := overlayName(base, branch)

	var (
		hits []searchengine.Hit
		err  error
	)
	switch {
	case overlay != base && cdeps.ovl != nil && shippedCompleteForUnifiedSearch(ctx, cdeps, base, branch):
		// The branch's own bucket covers the branch graph's whole embedded
		// population, so the base pool can only re-serve documents this pool
		// already holds. One pool, and it is the branch's.
		hits, err = cdeps.mgr.Search(ctx, kgtypes.GraphCode, overlay, query, queryVec, k)
	case overlay != base && cdeps.ovl != nil:
		hits, err = cdeps.ovl.SearchOverlay(ctx, kgtypes.GraphCode, base, overlay, query, queryVec, k)
	case overlay != base:
		// The request-level guard in composeCodeSearch rejects a branch search whose
		// engine lacks this arm — but it keys on the REQUEST's branch, which is empty
		// for a cross-repo fan-out (the interceptor never auto-fills a branch for
		// repo="all"). Per-repo branches are detected BELOW that guard, so a
		// cross-repo search against an engine without the overlay seam does reach
		// here in production, and recording a degrade is what makes that visible
		// instead of quietly serving a base-only result set for a branch query.
		slog.Error("code search: branch requested but the overlay arm is unavailable, reading the base pool alone",
			"graph", "code", "base", base, "branch", branch)
		cdeps.degrade.record("branch overlay arm unavailable, base pool only")
		hits, err = cdeps.mgr.Search(ctx, kgtypes.GraphCode, base, query, queryVec, k)
	default:
		hits, err = cdeps.mgr.Search(ctx, kgtypes.GraphCode, base, query, queryVec, k)
	}

	if err != nil {
		slog.Error("code search: segment engine search failed",
			"graph", "code", "base", base, "branch", branch, "error", err)
		cdeps.degrade.record("segment engine search failed for repo " + base)
		return nil
	}
	return hits
}

// shippedCompleteTTL bounds how long ONE (graph, branch) completeness verdict is
// reused before both operands are read again. The gate runs once per code query on
// the hot read path and NEITHER operand is free — the shipped count loads the L2
// read engine on the OSS rail and reads the manifest on the cloud rail
// (segmentdist/manager_coverage_probe.go:82-101), and the bar is a Stats RPC — so
// an unmemoized gate would put both on every query.
//
// 30 SECONDS, AND THE NUMBER IS CHOSEN FROM WHAT A STALE VERDICT COSTS IN EACH
// DIRECTION, not from a round figure. A stale NOT-COMPLETE costs one extra pool
// read per query until it expires: the union is the shape this path has always
// had, so the cost is the status quo. A stale COMPLETE is the one that can serve a
// short corpus, so it is the direction the window is sized for — a bucket cannot
// lose coverage faster than a ship or a delete lands, and 30s is under the interval
// at which either does. Longer would widen that exposure for a saving the memo has
// already collected; shorter would put the two reads back on the hot path.
const shippedCompleteTTL = 30 * time.Second

// shippedCompleteMemo holds one verdict per overlay-qualified graph name. The
// VERDICT is memoized rather than the two operands: the comparison is the whole
// product of reading them, and caching the operands separately would let a fresh
// covered count be compared against an expired bar.
//
// IT IS BOUNDED IN BOTH DIMENSIONS. The TTL bounds a verdict's age; the sweep on
// every miss bounds the map to the branches this process searched within one TTL,
// so a machine that churns through branch names does not accumulate an entry per
// branch forever.
var shippedCompleteMemo = struct {
	sync.Mutex
	at       map[string]time.Time
	complete map[string]bool
}{at: map[string]time.Time{}, complete: map[string]bool{}}

// cachedShippedComplete returns a verdict recorded within the TTL.
func cachedShippedComplete(overlay string, now time.Time) (bool, bool) {
	shippedCompleteMemo.Lock()
	defer shippedCompleteMemo.Unlock()
	at, ok := shippedCompleteMemo.at[overlay]
	if !ok || now.Sub(at) >= shippedCompleteTTL {
		return false, false
	}
	return shippedCompleteMemo.complete[overlay], true
}

// rememberShippedComplete records a verdict and evicts every expired sibling.
func rememberShippedComplete(overlay string, complete bool, now time.Time) {
	shippedCompleteMemo.Lock()
	defer shippedCompleteMemo.Unlock()
	for name, at := range shippedCompleteMemo.at {
		if now.Sub(at) >= shippedCompleteTTL {
			delete(shippedCompleteMemo.at, name)
			delete(shippedCompleteMemo.complete, name)
		}
	}
	shippedCompleteMemo.at[overlay] = now
	shippedCompleteMemo.complete[overlay] = complete
}

// shippedCompleteForUnifiedSearch reports whether a branch's OWN segment bucket
// holds the whole corpus a branch search must serve — the one condition under
// which reading the base pool alongside it adds nothing.
//
// THE TWO OPERANDS COME FROM DIFFERENT STAMPERS, AND THAT IS THE WHOLE
// CORRECTNESS OF THE GATE.
//
//   - covered is the SEGMENT ENGINE's DISTINCT LIVE-SEARCHABLE doc count for the
//     branch bucket (SegmentCoverageReader.LiveResidentDocCount) — how many
//     documents a search could actually return, counted ONCE each. It used to be
//     summed from the cloud rail's published metas; that rail is deleted, so the
//     local reading is the only one.
//   - the bar is the branch GRAPH's own embedded population — GraphStats
//     .BinaryVectorCount, the count of nodes carrying a stored binary vector,
//     maintained by the server's node store and read through the single
//     GraphEmbeddedCount definition.
//
// COUNTED ONCE EACH ON BOTH SIDES IS WHAT MAKES THE COMPARISON MEAN ANYTHING, and
// getting it wrong is how this gate served a partial corpus under a healthy banner.
// It previously read the SUMMING resident count, which counts an id resident in two
// segments TWICE — "the ordinary state after two rebuilds land without the first
// being retired". The bar counts each node ONCE. So a bucket holding Rd distinct
// documents against a bar of N read as complete whenever Rd + duplication >= N,
// i.e. a genuinely short bucket passed on the strength of its own duplication. The
// distinct-and-live reader removes that term: it cannot exceed the number of real
// documents, so covered >= bar now means what it says.
//
// THE FIX MOVES ONLY IN THE SAFE DIRECTION. The distinct count is less than or
// equal to the summing one, so this can turn a former "complete" into
// "not complete" and never the reverse — a bucket that stops collapsing pays one
// redundant base-pool read, which is the cost this gate exists to avoid paying
// WRONGLY.
//
// A bar derived from the resident pool would make this an IDENTITY, and that hazard
// is now UNIVERSAL rather than rail-specific. Both resident readers answer from the
// same engine the covered count comes from, so resident >= resident is true for a
// bucket that is half seeded, and the gate would report complete for exactly the
// corpus it exists to refuse. The server's counter is the one authority the segment
// engine does not write, which is why the bar must keep coming from there.
//
// THE COMPARISON IS NON-SHORTFALL WITH NO TOLERANCE, and the zero is earned rather
// than assumed. The bar counts precisely the nodes that CAN appear in the HNSW
// corpus — a node with no vector is in neither population — so the two numbers are
// commensurable and any shortfall is a real gap. That pairing is not invented here:
// the covered count sums only the HNSW dimension because it "mirrors the graph's
// binary_vector_count denominator" (manager_coverage_probe.go:52-54). A tolerance
// would be the one knob that could let a genuinely short bucket through.
//
// EVERY UNKNOWN READS AS NOT COMPLETE, because the two-pool union is the safe
// direction and a wrong "complete" serves a partial corpus under a healthy banner:
// a read error on either operand, an unwired coverage seam, and a bar of zero —
// which a router-less caller gets back from GraphEmbeddedCount, and which would
// otherwise make covered >= 0 the same always-true identity arriving by a different
// door.
//
// ONE UNKNOWN CLASS IS GONE ENTIRELY rather than being handled here: the
// conservative-unknown signal that fired when a shipped segment predated the
// doc_count field. Its source was the manifest read, and no engine reports that
// sentinel, so the term was removed rather than left as a permanently-true conjunct
// that would read as a live guard.
//
// THE BAR IS ADDRESSED AT THE BRANCH, and that is what makes a sparse branch
// safe without a special case. The server resolves a Branch-carrying code selector
// through Scope, which serves a FULL branch from its own layer alone and a sparse
// one as base plus overlay. So a full branch is measured against its own
// population, and a sparse branch is measured against BASE's much larger one — a
// short bucket cannot meet it, and the union stays. A half-finished seed fails the
// same way, which is the discrimination a "was seeded" flag would have destroyed.
func shippedCompleteForUnifiedSearch(ctx context.Context, cdeps codeSearchDeps, base, branch string) bool {
	if cdeps.cov == nil || cdeps.gc == nil {
		return false
	}
	overlay := overlayName(base, branch)
	now := time.Now()
	if verdict, cached := cachedShippedComplete(overlay, now); cached {
		return verdict
	}

	// THIS CALL IS FOR ITS LOAD AND ITS ERROR, NOT FOR ITS NUMBER. It is what
	// materializes the bucket's engine (the reader loads before answering) and it is
	// the only "could this operand be read at all" signal the gate has. The COUNT it
	// returns is the summing one, which is not commensurable with the bar — see the
	// block above — so it is deliberately discarded and the distinct count is read
	// below off the engine this call just loaded.
	//
	// IF A FUTURE CHANGE MAKES THIS READER NON-LOADING, the distinct read below sees
	// an unloaded pool and answers 0, the gate reports not-complete, and the two-pool
	// union stays. That is the safe direction — it costs a redundant base read, never
	// a partial corpus served as whole — but it is a silent behaviour change, so the
	// coupling is named here rather than left to be rediscovered.
	_, err := cdeps.cov.ShippedSegmentDocCount(ctx, kgtypes.GraphCode, overlay)
	complete := err == nil
	if complete {
		covered := cdeps.cov.LiveResidentDocCount(kgtypes.GraphCode, overlay)
		target := graphsel.GraphSelectorFor(kgtypes.GraphCode, base, false)
		target.Branch = branch
		embedded, barErr := graphEmbeddedCountFor(ctx, cdeps.gc, target)
		complete = barErr == nil && embedded > 0 && covered >= embedded
	}
	rememberShippedComplete(overlay, complete, now)
	return complete
}

// multiRepoBranch resolves ONE repo's branch for the cross-repo fan-out. Each
// repo is detected independently from its own machine-local checkout, because a
// single branch name shared across the fan-out is wrong twice over: two repos are
// routinely on different branches, and a branch that exists in one repo need not
// exist in another.
//
// THE PARTITION IS THREE STATES, NOT TWO, and collapsing it is the failure worth
// naming. A repo with NO manifest entry has no local checkout on this machine, so
// the base graph is the COMPLETE answer and there is no overlay to miss — it
// yields no branch and records NOTHING. A repo whose manifest entry names a
// checkout that could not be read is different in kind: an overlay may exist and
// we could not find out, so it degrades and names the repo. Recording the first
// state as well would print the banner on every cross-repo search on any machine
// holding graphs it never collected locally, and a warning that always fires is
// not a truthful warning.
//
// A detection miss NEVER fails the fan-out — one repo's unreadable checkout must
// not cost the caller every other repo's results.
func multiRepoBranch(ctx context.Context, repo string, d *searchDegrade) string {
	branch, state := autoDetectBranchReason(ctx, repo)
	if state == branchDetectFailed {
		d.record("branch detection failed for repo " + repo)
	}
	return branch
}
