// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// cacheOp is one recorded operation against the instrumented cache, in call order.
type cacheOp struct {
	kind string // "put" | "remove" | "get"
	id   searchengine.SegmentID
}

// instrumentedCache is a segmentL2Cache seam that WRAPS a real *diskSegmentCache
// (so on-disk state stays authoritative and the engine still searches real bytes)
// while recording the order of Put/Remove/Get and offering a fault-injection hook
// fired BETWEEN a Put and the Removes that follow it. It is the substitutable
// implementation the segmentL2Cache interface (Phase 2 seam) exists for: the
// prune-safety tests assert Put-before-Remove ordering and simulate a crash in
// the reclaim window that buildManager's internally-constructed cache cannot.
type instrumentedCache struct {
	inner *diskSegmentCache

	mu  sync.Mutex
	ops []cacheOp

	// beforeRemove, when set, is invoked the FIRST time a Remove is about to run
	// (i.e. after the merged Put already landed). It models a crash in the
	// Put-then-Remove window: the test can panic, stop the world, or flip a flag.
	// Cleared after firing once so only the first Remove triggers it.
	beforeRemove func()
	fired        bool

	// blockRemove, when true, makes Remove a no-op (records the op but does NOT
	// touch disk) — modeling a crash that halts AFTER the Put but BEFORE any
	// constituent file is actually deleted.
	blockRemove bool

	// blockPut, when true, makes Put a no-op (records the op but does NOT touch
	// disk) — modeling a crash that halts BEFORE the merged blob is persisted. The
	// crash-safe ordering means the constituents are then still intact on disk.
	blockPut bool

	// removeAfterKeys, when set, is the id the cache evicts from the inner store
	// the FIRST time Keys() is called, AFTER Keys has already captured (and
	// returned) the full snapshot. This deterministically models the
	// reclaimMerged/InvalidateLocal Remove that races the load()'s Keys() snapshot:
	// the id is present in the returned snapshot but is a cache MISS by the time
	// reload()'s Get reaches it. Cleared after firing once.
	removeAfterKeys searchengine.SegmentID
}

func newInstrumentedCache(inner *diskSegmentCache) *instrumentedCache {
	return &instrumentedCache{inner: inner}
}

func (c *instrumentedCache) Get(id searchengine.SegmentID) ([]byte, bool) {
	c.mu.Lock()
	c.ops = append(c.ops, cacheOp{kind: "get", id: id})
	c.mu.Unlock()
	return c.inner.Get(id)
}

func (c *instrumentedCache) Put(id searchengine.SegmentID, b []byte) {
	c.mu.Lock()
	c.ops = append(c.ops, cacheOp{kind: "put", id: id})
	block := c.blockPut
	c.mu.Unlock()
	if block {
		return // crash before the merged blob is persisted
	}
	c.inner.Put(id, b)
}

func (c *instrumentedCache) Remove(id searchengine.SegmentID) {
	c.mu.Lock()
	c.ops = append(c.ops, cacheOp{kind: "remove", id: id})
	hook := c.beforeRemove
	if hook != nil && !c.fired {
		c.fired = true
		c.mu.Unlock()
		hook() // may panic (crash model) — runs BEFORE the disk delete
		c.mu.Lock()
	}
	block := c.blockRemove
	c.mu.Unlock()
	if block {
		return // crash before the actual file delete
	}
	c.inner.Remove(id)
}

// Keys forwards to the inner cache's in-memory index enumeration. Records the op
// so a test can assert load()'s L2 fallback enumerated the resident set. When
// removeAfterKeys is armed, it evicts that id from the inner cache AFTER capturing
// the snapshot — modeling a Remove that races the Keys() snapshot (the id is in
// the returned slice but a miss at the later Get).
func (c *instrumentedCache) Keys() []searchengine.SegmentID {
	c.mu.Lock()
	c.ops = append(c.ops, cacheOp{kind: "keys"})
	c.mu.Unlock()
	snap := c.inner.Keys()
	c.mu.Lock()
	raced := c.removeAfterKeys
	c.removeAfterKeys = ""
	c.mu.Unlock()
	if raced != "" {
		c.inner.Remove(raced)
	}
	return snap
}

// opLog returns a copy of the recorded operations in call order.
func (c *instrumentedCache) opLog() []cacheOp {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]cacheOp, len(c.ops))
	copy(out, c.ops)
	return out
}

// firstIndex returns the index of the first op matching kind (and id, when id !=
// ""), or -1.
func (c *instrumentedCache) firstIndex(kind, id searchengine.SegmentID) int {
	for i, op := range c.opLog() {
		if op.kind == kind && (id == "" || op.id == id) {
			return i
		}
	}
	return -1
}

// removedSet returns the set of ids the cache has ever been asked to Remove —
// the removedSoFar input clause-2 of assertLiveSetBackedByL2 cross-checks against
// the live Export() set.
func (c *instrumentedCache) removedSet() map[searchengine.SegmentID]struct{} {
	out := make(map[searchengine.SegmentID]struct{})
	for _, op := range c.opLog() {
		if op.kind == "remove" {
			out[op.id] = struct{}{}
		}
	}
	return out
}
