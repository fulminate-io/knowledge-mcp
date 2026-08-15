// SPDX-License-Identifier: Apache-2.0

// code_search_pools.go — pool selection for one code query: whether this search
// reads a single repo pool or a base pool plus its branch overlay, and how many
// candidates it asks the engine for.

package tools

import (
	"context"
	"log/slog"

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
// fusing. A default-branch search reads the single repo pool. The two-pool arm
// itself decides what a failing pool means: a base-pool failure fails the search,
// an overlay-pool failure degrades to the base pool.
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
