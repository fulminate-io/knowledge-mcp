// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// reclaimMerged is the engine merge-completion handler: when a background merge
// consolidates several segments into one, it reclaims the superseded
// constituents' L2 disk files from THIS engine's LIVE cache. It is registered as
// the engine's Options.OnMerge at construction (managers/bm25Managers only — the
// deterministic rebuild engines get nil OnMerge and reclaim via the existing
// FlushDeterministic→InvalidateLocal path instead).
//
// CRASH-SAFE ORDERING (the load-bearing logic): the merged blob is Put FIRST,
// then the constituents are Removed. doMerge does NOT persist the merged blob —
// the only on-disk copies are the old constituents — so Putting the merged blob
// before Removing the constituents guarantees a crash at any point leaves either
// {constituents present} OR {merged present} on disk, never NEITHER. The reverse
// order would open a window where a crash between Remove and Put loses the docs
// entirely (a fresh false-prune). An empty Merged.ID means doMerge could not
// encode the consolidated blob; in that case we skip the whole reclaim — a Remove
// without a durable Put is exactly the false-prune we are guarding against.
//
// It touches ONLY the cache (no shippedIDs/locallyShipped/resident bookkeeping):
// merged-away constituents that were shipped are reconciled server-side
// separately — the embed path's next shipAndPublish republishes the post-merge
// RESIDENT Export() as this writer's manifest (which no longer contains the
// merged-away ids), so the server reference-count-GCs them; the deterministic
// ROLE-A rebuild reconciles via its reconcilePrune leg. This handler is purely
// L2-disk reclamation. No extra locking is needed — diskSegmentCache.Put/Remove
// are internally mutex-guarded.
func (m *distManager[Q, S]) reclaimMerged(res searchengine.MergeResult) {
	if res.Merged.ID == "" {
		return // no durable merged blob to anchor the reclaim — do not Remove
	}
	// (a) Persist the consolidated blob FIRST (the crash-safe anchor). Reuses the
	// shipNew Put idiom (manager_prune.go).
	m.cache.Put(res.Merged.ID, res.Merged.Bytes)
	// (b) Then reclaim the superseded constituents from the LIVE cache. Reuses the
	// InvalidateLocal Remove-loop idiom (manager_owner.go) but targets the live
	// embed cache, not the deterministic rebuild cache.
	for _, id := range res.Removed {
		m.cache.Remove(id)
	}
}
