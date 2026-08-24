// SPDX-License-Identifier: Apache-2.0

// code_search_pools_evicted_test.go — partition entry B1. The unified-search
// completeness gate is SEARCH-VISIBLE (a false verdict sends the query down the
// two-pool union path instead of the branch-only path) and its verdict is CACHED,
// so a coverage read that declined for an evicted pool would change search results
// for that overlay and keep changing them past re-materialization.
//
// THE FENCE THAT PREVENTS IT LIVES IN segmentdist, NOT HERE:
// Manager.LoadResidentDocCount keeps its materializing load(), so the reader this
// gate consults answers the same before and after an eviction. That fence is pinned
// by its own two gates — the structural one over LoadResidentDocCount's region, and
// the behavioral resident_doc_count_materializes subtest of
// TestBackgroundArmsDoNotResurrectAnEvictedPool, both in segmentdist.
//
// What THIS file pins is the tools-side consequence, in both directions: given a
// MATERIALIZING reader the verdict and the cached verdict survive an evict/reload
// cycle unchanged, and given a DECLINING reader — the shape the fence forbids —
// they do not. The second half is what makes the first half a measurement rather
// than a restatement of the fake.

package tools

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// evictableCoverageFake models the segment manager's coverage read over a pool that
// can be evicted. materializes selects WHICH contract it implements:
//
//   - true  — the landed contract: the read re-materializes an evicted pool and
//     reports the real covered count, so eviction is invisible to this gate.
//   - false — the contract partition entry B1 forbids: an evicted pool declines and
//     the read reports 0.
type evictableCoverageFake struct {
	mu           sync.Mutex
	covered      int
	evicted      bool
	materializes bool
	reads        int
}

func (f *evictableCoverageFake) evict() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evicted = true
}

func (f *evictableCoverageFake) isEvicted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.evicted
}

func (f *evictableCoverageFake) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func (f *evictableCoverageFake) ShippedSegmentDocCount(
	_ context.Context, _ kgtypes.GraphType, _ string,
) (int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.evicted {
		if !f.materializes {
			return 0, false, nil // the declining contract: a zero nobody measured
		}
		f.evicted = false // the landed contract: the read materializes the pool
	}
	return f.covered, false, nil
}

// The rest of the seam is unread by this gate.
func (f *evictableCoverageFake) ResidentDocCount(kgtypes.GraphType, string) int     { return 0 }
func (f *evictableCoverageFake) LiveResidentDocCount(kgtypes.GraphType, string) int { return 0 }
func (f *evictableCoverageFake) RepairVerification(kgtypes.GraphType, string) (RepairVerification, bool) {
	return RepairVerification{}, false
}

func (f *evictableCoverageFake) LoadRebuildState(kgtypes.GraphType, string) (int64, []searchengine.ExternalID, error) {
	return 0, nil, nil
}
func (f *evictableCoverageFake) LoadMergeWatermark(kgtypes.GraphType, string) (int64, error) {
	return 0, nil
}

// forgetShippedComplete drops one overlay's memoized verdict, standing in for the
// TTL expiring between the two reads. Without it the second call is served from the
// memo and measures nothing.
func forgetShippedComplete(overlay string) {
	shippedCompleteMemo.Lock()
	defer shippedCompleteMemo.Unlock()
	delete(shippedCompleteMemo.at, overlay)
	delete(shippedCompleteMemo.complete, overlay)
}

// TestUnifiedSearchVerdictSurvivesAnEvictReloadCycle is ticket constraint 6 applied
// to the one verdict a searcher can actually see the difference in.
func TestUnifiedSearchVerdictSurvivesAnEvictReloadCycle(t *testing.T) {
	ctx := context.Background()
	const repo, branch = "evicted-repo", "feat"
	overlay := overlayName(repo, branch)

	newDeps := func(materializes bool) (codeSearchDeps, *evictableCoverageFake) {
		cov := &evictableCoverageFake{covered: 120, materializes: materializes}
		return codeSearchDeps{cov: cov, gc: &poolBarFake{embedded: 100}}, cov
	}

	t.Run("materializing_reader_keeps_the_verdict", func(t *testing.T) {
		forgetShippedComplete(overlay)
		t.Cleanup(func() { forgetShippedComplete(overlay) })
		deps, cov := newDeps(true)

		before := shippedCompleteForUnifiedSearch(ctx, deps, repo, branch)
		require.True(t, before, "PRECONDITION: a covered overlay reads complete")

		cov.evict()
		forgetShippedComplete(overlay)

		after := shippedCompleteForUnifiedSearch(ctx, deps, repo, branch)
		require.Equal(t, before, after,
			"the completeness verdict must be identical across an evict/reload cycle")
		require.False(t, cov.isEvicted(), "the consumer-side read re-materialized the pool")
		require.Equal(t, 2, cov.readCount(), "both verdicts came from a real read, not the memo")

		cached, ok := cachedShippedComplete(overlay, time.Now())
		require.True(t, ok, "the second verdict was memoized")
		require.True(t, cached, "and the memoized verdict is the TRUE one, not a poisoned false")
	})

	t.Run("declining_reader_flips_and_poisons_the_verdict", func(t *testing.T) {
		// THE KNOWN-NEGATIVE CONTROL, and the reason the assertions above are a
		// measurement. This is the contract partition entry B1 forbids: it changes the
		// search's pool shape for an evicted overlay AND caches that wrong verdict past
		// re-materialization.
		forgetShippedComplete(overlay)
		t.Cleanup(func() { forgetShippedComplete(overlay) })
		deps, cov := newDeps(false)

		before := shippedCompleteForUnifiedSearch(ctx, deps, repo, branch)
		require.True(t, before)

		cov.evict()
		forgetShippedComplete(overlay)

		after := shippedCompleteForUnifiedSearch(ctx, deps, repo, branch)
		require.False(t, after,
			"a declining read flips the verdict — a DIFFERENT search result for an evicted overlay")
		require.NotEqual(t, before, after)

		cached, ok := cachedShippedComplete(overlay, time.Now())
		require.True(t, ok)
		require.False(t, cached, "and the wrong verdict is cached, so it survives re-materialization")
	})
}
