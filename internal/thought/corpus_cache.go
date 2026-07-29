// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// corpus_cache.go holds the daemon-resident thought-corpus cache: the
// live node set plus the per-layer keyset cursors the O(delta) drain advances.
// The cache is cold-drained once, then delta-merged on each dirty tick and
// reconciled against the server probe — replacing the hourly full re-drain.
//
// WIRE SHAPE NOTE: CorpusDeltaResponse.items is a FLAT []Node (no per-item layer
// stamp), so the cache stores the live set in ONE id-keyed map. Overlay-over-base
// dedupe falls out of the merge ORDER: the server emits base-layer items first,
// then each overlay, so a later overlay upsert overwrites the base version by id —
// exactly the overlay-wins Snapshot semantics. The per-layer KEYSET cursors
// (advanced from resp.NextCursors, which ARE layer-keyed on the wire) and the
// per-layer probes drive reconciliation.

// layerCursor is one layer's tombstone-INCLUSIVE keyset high-water — it advances
// past tombstoned rows too (they carry the newest updated_at), so a delete never
// strands a live row behind the cursor.
type layerCursor struct {
	afterUpdatedAt int64
	afterID        string
}

// corpusCache is the resident live thought-corpus + per-layer cursors.
type corpusCache struct {
	mu      sync.Mutex
	live    map[string]*knowledgev1.Node // id → live node (tombstoned rows removed)
	cursors map[string]layerCursor       // layer_key → keyset high-water
}

// newCorpusCache builds an empty cache.
func newCorpusCache() *corpusCache {
	return &corpusCache{
		live:    make(map[string]*knowledgev1.Node),
		cursors: make(map[string]layerCursor),
	}
}

// MergeDelta applies one delta page to the cache: upsert live items by id, REMOVE
// items whose tombstoned_at != 0 (deletes propagate), and advance each per-layer
// cursor from resp.NextCursors (tombstone-inclusive high-water). A RESURRECT — a
// tombstone then a later live re-create of the same id — is handled naturally: the
// live re-add restores the id to the map after the tombstone removed it.
func (c *corpusCache) MergeDelta(resp *knowledgev1.CorpusDeltaResponse) {
	if resp == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range resp.GetItems() {
		if n.GetTombstonedAt() != 0 {
			delete(c.live, n.GetId())
			continue
		}
		c.live[n.GetId()] = n
	}
	for _, cur := range resp.GetNextCursors() {
		c.cursors[cur.GetLayerKey()] = layerCursor{
			afterUpdatedAt: cur.GetAfterUpdatedAt(),
			afterID:        cur.GetAfterId(),
		}
	}
}

// Cursors returns the current per-layer cursors as wire LayerCursor messages, for
// threading back into the next drain's request.
func (c *corpusCache) Cursors() []*knowledgev1.LayerCursor {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*knowledgev1.LayerCursor, 0, len(c.cursors))
	for key, cur := range c.cursors {
		out = append(out, &knowledgev1.LayerCursor{
			LayerKey:       key,
			AfterUpdatedAt: cur.afterUpdatedAt,
			AfterId:        cur.afterID,
		})
	}
	return out
}

// Snapshot returns the merged live node set (one entry per distinct id —
// overlay-over-base already resolved at merge time).
func (c *corpusCache) Snapshot() []*knowledgev1.Node {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*knowledgev1.Node, 0, len(c.live))
	for _, n := range c.live {
		out = append(out, n)
	}
	return out
}

// Reset clears the cache (forced full resync / cold start).
func (c *corpusCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.live = make(map[string]*knowledgev1.Node)
	c.cursors = make(map[string]layerCursor)
}

// Reconcile reports whether the cache is CONSISTENT with the server probe served
// at resp.SafeHorizon (H). It returns false ⟹ the caller forces a full resync.
//
// T2-1 CORRECTED MATH — the naive comparisons spuriously mismatch because H is
// non-monotonic and probe.max_updated_at is tombstone-inclusive:
//
//   - COUNT: compare Σ probe.LiveCount to the count of cached-live rows with
//     updated_at <= H. Filtering the cache by <= H is REQUIRED: a REGRESSED H
//     (lower than a prior tick's) leaves cached rows above it that the probe does
//     NOT count — comparing against the unfiltered cache size would spuriously
//     mismatch on every H regression.
//   - HIGH-WATER: compare each probe.MaxUpdatedAt to that layer's CURSOR
//     high-water (tombstone-INCLUSIVE), NOT the cached LIVE-set max. On a
//     DELETE-tick MergeDelta removed the tombstone from the live cache, so
//     cache-live-max < probe.max (which counts the tombstone's bumped updated_at)
//     — comparing against the live max would force a spurious resync on every
//     delete. The cursor high-water advanced past the tombstone, so it matches the
//     probe's tombstone-inclusive max exactly. The check is H-AWARE: when a
//     REGRESSED H sits BELOW the cursor high-water (the cursor drained to a higher
//     prior H), probe.MaxUpdatedAt is bounded by the lower H and cannot equal the
//     cursor — so the high-water equality is SKIPPED in that case (the count check,
//     itself filtered by <= H, carries reconciliation at the regressed horizon; the
//     high-water was already validated at the earlier higher H).
//
// Both hold clean across delete-ticks, H-regression, resurrect-ticks, and
// mixed-ticks; a genuine missed change (count divergence, or high-water divergence
// when the cursor is at/below H) returns false.
func (c *corpusCache) Reconcile(resp *knowledgev1.CorpusDeltaResponse) bool {
	if resp == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	horizon := resp.GetSafeHorizon()
	var cachedLiveLEH int64
	for _, n := range c.live {
		if n.GetUpdatedAt() <= horizon {
			cachedLiveLEH++
		}
	}
	var probeLive int64
	for _, p := range resp.GetLayerProbes() {
		probeLive += p.GetLiveCount()
		cur := c.cursors[p.GetLayerKey()]
		// High-water equality only when the cursor is at/below H. A cursor ABOVE H
		// means H regressed below what the cache already drained — the probe's
		// H-bounded max cannot equal the higher cursor, so skip (not a divergence).
		if cur.afterUpdatedAt <= horizon && p.GetMaxUpdatedAt() != cur.afterUpdatedAt {
			return false
		}
	}
	return probeLive == cachedLiveLEH
}
