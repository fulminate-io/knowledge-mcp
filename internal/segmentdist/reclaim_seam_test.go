// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"errors"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// cacheOp is one recorded operation against the instrumented cache, in call order.
type cacheOp struct {
	kind string // "put" | "remove" | "get" | "getmapped"
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

	// failMapping, when true, makes GetMapped report a MAPPING FAILURE for an id
	// that is genuinely cached. It models a broken platform mapping arm — the
	// state in which a silent fall back to the heap read would hide the breakage
	// on exactly the platform CI never runs.
	failMapping bool

	// failPut makes Put report a WRITE FAILURE — distinct from blockPut, which
	// models a crash and reports nothing. failPutFrom selects which call starts
	// failing (1-based, zero means from the first), so a test can let an initial
	// write land and fail a LATER one: a copy or a multi-blob write that aborts
	// only when the very first blob fails would pass a from-the-start fixture
	// while still leaving a partial result on any other ordering.
	failPut     bool
	failPutFrom int
	putCalls    int

	// failPutUntil makes the FIRST failPutUntil Put calls report a WRITE FAILURE and
	// every call after them succeed — the TRANSIENT disk error a bounded retry is
	// meant to absorb. It is the COMPLEMENT of failPutFrom, not a duplicate of it:
	// failPutFrom fails from a call onward and never recovers, so a fixture built on
	// it can only ever exercise an EXHAUSTED retry and cannot tell a retry that
	// succeeded on a later attempt from one that was never attempted at all.
	failPutUntil int

	// failPutSkipFirst exempts the first failPutSkipFirst Put calls from failPut and
	// failPutUntil entirely; the injection then counts from the call after them.
	//
	// IT EXISTS BECAUSE A DELETE'S PUTS HAVE TWO PRODUCERS, and which one a
	// call-ordinal names decides what a test is measuring. The group swap's merge hook
	// (reclaimMerged) Puts the consolidated blob FIRST, and persistResident's write
	// follows it, so a from-the-start injection hits the RECLAIM and a test aiming at
	// the write's retry silently exercises an aborted reclaim instead. Skipping the
	// leading reclaim Put is what lets a fixture target the second producer alone. A
	// test using it must assert the skip landed where it meant — a completed reclaim
	// is observable as a non-empty removedSet.
	failPutSkipFirst int
}

// errInjectedMappingFailure is the failure failMapping injects.
var errInjectedMappingFailure = errors.New("injected mapping failure")

// errInjectedPutFailure is the failure failPut injects. Tests assert on it by
// identity (errors.Is) rather than on a message, so a caller that wraps it still
// satisfies the assertion while a caller that invents its own error does not.
var errInjectedPutFailure = errors.New("injected put failure")

func newInstrumentedCache(inner *diskSegmentCache) *instrumentedCache {
	return &instrumentedCache{inner: inner}
}

func (c *instrumentedCache) Get(id searchengine.SegmentID) ([]byte, bool) {
	c.mu.Lock()
	c.ops = append(c.ops, cacheOp{kind: "get", id: id})
	c.mu.Unlock()
	return c.inner.Get(id)
}

// GetMapped mirrors Get through the instrumentation, and additionally lets a
// test force the MAPPING to fail while the id is genuinely cached — the one
// condition the reload path must surface rather than answer as a miss.
func (c *instrumentedCache) GetMapped(id searchengine.SegmentID) ([]byte, func(), bool, error) {
	c.mu.Lock()
	c.ops = append(c.ops, cacheOp{kind: "getmapped", id: id})
	fail := c.failMapping
	c.mu.Unlock()
	if fail {
		return nil, nil, false, errInjectedMappingFailure
	}
	return c.inner.GetMapped(id)
}

// Put mirrors the write through the instrumentation and PROPAGATES the inner
// cache's error, because this cache is the only segment store and a discarded Put
// error is a segment the engine believes it wrote.
//
// blockPut AND failPut MODEL DIFFERENT FAILURES, and collapsing them would lose the
// distinction the reclaim tests turn on. blockPut is a CRASH: the write never
// happens and nothing reports it, so Put returns nil and the caller proceeds
// believing the blob landed — that is the pre-existing crash-window model and its
// nil return is deliberate. failPut is a WRITE THAT FAILED AND SAID SO: it returns
// an error, which is the condition the fail-loud change exists to propagate. A test
// that used blockPut to exercise the error path would prove nothing, because a
// crash model never produces an error to propagate.
func (c *instrumentedCache) Put(id searchengine.SegmentID, parts ...[]byte) error {
	c.mu.Lock()
	c.ops = append(c.ops, cacheOp{kind: "put", id: id})
	block := c.blockPut
	fail := c.failPut
	failFrom := c.failPutFrom
	failUntil := c.failPutUntil
	skipFirst := c.failPutSkipFirst
	c.putCalls++
	// The INJECTION ordinal, which is the raw call ordinal only when nothing is
	// skipped. A non-positive value means this call is one of the exempt leading
	// writes and no injection rule applies to it.
	n := c.putCalls - skipFirst
	c.mu.Unlock()
	if n > 0 && fail && n >= failFrom {
		return errInjectedPutFailure
	}
	if n > 0 && n <= failUntil {
		return errInjectedPutFailure
	}
	if block {
		return nil // crash before the merged blob is persisted: no error is raised
	}
	// THE PARTS ARE FORWARDED UNCHANGED. A double that concatenated them, or that
	// forwarded only one, would write a different file than production writes and
	// would hide exactly the arity defect the variadic shape makes possible.
	return c.inner.Put(id, parts...)
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

// sizeOf forwards to the inner cache's recency-neutral index probe. It records NO
// op: the residency gate calls it once per resident id, and logging that would
// swamp the ordering assertions the op log exists for.
func (c *instrumentedCache) sizeOf(id searchengine.SegmentID) (int64, bool) {
	return c.inner.sizeOf(id)
}

// putCallCount reports how many Put calls the cache has served, FAILED ONES
// INCLUDED. Counting ATTEMPTS rather than successes is the point: a retry is a
// second attempt at a write that did not land, and a success-only counter cannot
// see one at all.
func (c *instrumentedCache) putCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.putCalls
}

// maxPutsPerID reports the largest number of Put calls any SINGLE segment id
// received. It is the retry question asked exactly: writing one blob more than once
// is a retry, writing several blobs once each is not.
//
// PER-ID RATHER THAN TOTAL, because a partition re-emit's total is not attributable.
// The group rebuild's merge hook reclaims through the same cache (reclaimMerged), so
// a delete's Put calls come from two producers and a whole-cache total cannot say
// which one repeated.
func (c *instrumentedCache) maxPutsPerID() int {
	per := make(map[searchengine.SegmentID]int)
	most := 0
	for _, op := range c.opLog() {
		if op.kind != "put" {
			continue
		}
		per[op.id]++
		most = max(most, per[op.id])
	}
	return most
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
