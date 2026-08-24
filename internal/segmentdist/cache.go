// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"container/list"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// diskSegmentCache is the CLIENT-side L2 disk cache: a content-addressed
// on-disk store of segment blobs under <root>/<id>.seg, where id IS the sha256
// content hash — a self-verifying filename, so a restart re-loads from disk not
// the network (the contract's "persist pulled segment blobs locally so a restart
// re-loads from disk"). A cache HIT skips the network on the next Source.Fetch.
//
// Eviction: bounded TOTAL on-disk bytes with LRU eviction. On Put, if the total
// cached bytes exceed the cap, evict the least-recently-used .seg files until
// under cap. Because segments are immutable + content-addressed, eviction is
// always safe — a re-Fetch re-populates the entry. Get updates recency.
//
// Concurrency: a mutex guards the in-memory LRU index + total-bytes counter; the
// actual file IO (read on Get, write+rename on Put) happens under the lock for
// simplicity and correctness — the cache is an L2 backstop, not a hot read path
// (the engine's resident segment set is the hot path). Atomic writes roll their
// OWN temp+rename here: the server's store.atomicWriteFile lives in a SEPARATE
// module and cannot be imported.
type diskSegmentCache struct {
	root    string
	maxByte int64
	// advice is the read-ahead hint every mapping this cache opens carries. It
	// is per-cache because a cache is constructed per (graph, format), so the
	// instance already knows its format — which is why GetMapped does NOT take
	// it: widening that would change the searchengine.SegmentCache interface and
	// every test double for no gain.
	advice readAdvice

	mu     sync.Mutex
	ll     *list.List               // front = MRU, back = LRU; element value = *cacheEntry
	index  map[string]*list.Element // id -> element
	curByt int64
}

// cacheEntry is one LRU node: the content-hash id + the stored byte size.
type cacheEntry struct {
	id    string
	bytes int64
}

var _ searchengine.SegmentCache = (*diskSegmentCache)(nil)

// *diskSegmentCache also satisfies the wider segmentL2Cache seam (Get/Put/Remove)
// distManager writes through — the Remove method is what segmentL2Cache adds over
// searchengine.SegmentCache.
var _ segmentL2Cache = (*diskSegmentCache)(nil)

// newDiskSegmentCache constructs a content-addressed L2 cache rooted at root
// with a maximum total on-disk byte budget of maxBytes (<= 0 means unbounded).
// On construction it scans the root once so a restart recovers the prior cache
// state (size + LRU membership) and re-loads blobs from disk rather than the
// network. The dir is created lazily on the first Put.
func newDiskSegmentCache(root string, maxBytes int64, advice readAdvice) *diskSegmentCache {
	c := &diskSegmentCache{
		root:    root,
		maxByte: maxBytes,
		advice:  advice,
		ll:      list.New(),
		index:   make(map[string]*list.Element),
	}
	c.scanExisting()
	return c
}

// scanExisting recovers cache membership from the root dir on construction so a
// fresh process Gets a prior id from disk (restart re-load). Recency ordering of
// recovered entries is arbitrary (insertion order); a Get re-establishes MRU.
func (c *diskSegmentCache) scanExisting() {
	entries, err := os.ReadDir(c.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".seg" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := name[:len(name)-len(".seg")]
		el := c.ll.PushFront(&cacheEntry{id: id, bytes: info.Size()})
		c.index[id] = el
		c.curByt += info.Size()
	}
}

// path returns the .seg file path for a content-hash id.
func (c *diskSegmentCache) path(id string) string {
	return filepath.Join(c.root, id+".seg")
}

// Get returns the cached bytes for id and updates recency (MRU). A miss returns
// (nil, false). The in-memory index and the on-disk file are kept consistent: a
// stale index entry whose file vanished is dropped and reported as a miss.
func (c *diskSegmentCache) Get(id searchengine.SegmentID) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[id]
	if !ok {
		return nil, false
	}
	data, err := os.ReadFile(c.path(id)) //nolint:gosec // id is a content hash with no separators (engine-supplied)
	if err != nil {
		// File disappeared underneath us — drop the stale index entry.
		c.removeElement(el)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return data, true
}

// GetMapped returns the cached blob as a read-only MEMORY MAPPING plus the
// closure that releases it, and updates recency exactly as Get does. It is the
// variant the resident read path uses: the bytes become OS page cache the
// kernel can reclaim rather than Go heap the collector must scan.
//
// A miss (ok=false, nil error) means the id is not cached. A non-nil error means
// the id IS cached but its file could not be mapped, and the caller must surface
// that rather than treat it as a miss — answering it as a miss would route the
// caller to a heap-reading path and hide a broken mapping seam behind a slow one.
// A stale index entry whose file has vanished is dropped and reported as a plain
// miss, matching Get: that is index/disk reconciliation, not a mapping failure.
func (c *diskSegmentCache) GetMapped(id searchengine.SegmentID) ([]byte, func(), bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[id]
	if !ok {
		return nil, nil, false, nil
	}
	path := c.path(id)
	if _, err := os.Stat(path); err != nil {
		c.removeElement(el)
		return nil, nil, false, nil
	}
	m, err := mapBlobFile(path, c.advice)
	if err != nil {
		return nil, nil, false, fmt.Errorf("segmentdist: map cached segment %s: %w", id, err)
	}
	c.ll.MoveToFront(el)
	return m.data, func() { _ = m.release() }, true, nil
}

// Put writes b under id (content-addressed) with an atomic temp+rename, updates
// recency, and evicts LRU entries until total bytes are under the cap. A repeated
// Put of the same id refreshes recency without double-counting bytes. Errors are
// swallowed (the cache is a best-effort backstop; a failed Put just means the
// next Get is a miss and re-Fetches).
func (c *diskSegmentCache) Put(id searchengine.SegmentID, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.index[id]; ok {
		// Already cached (immutable content) — just refresh recency.
		c.ll.MoveToFront(el)
		return
	}

	if err := os.MkdirAll(c.root, 0o750); err != nil {
		return
	}
	if err := atomicWriteFile(c.path(id), b); err != nil {
		return
	}

	el := c.ll.PushFront(&cacheEntry{id: id, bytes: int64(len(b))})
	c.index[id] = el
	c.curByt += int64(len(b))
	c.evictLocked()
}

// evictLocked drops least-recently-used entries until total bytes are under the
// cap. Caller holds c.mu. A non-positive cap disables eviction.
func (c *diskSegmentCache) evictLocked() {
	if c.maxByte <= 0 {
		return
	}
	for c.curByt > c.maxByte {
		back := c.ll.Back()
		if back == nil {
			return
		}
		c.removeElement(back)
	}
}

// removeElement deletes an LRU element from the index + list, removes its file,
// and decrements the byte counter. Caller holds c.mu.
func (c *diskSegmentCache) removeElement(el *list.Element) {
	ent, ok := el.Value.(*cacheEntry)
	if !ok {
		// The list only ever holds *cacheEntry; a failed cast is corruption.
		c.ll.Remove(el)
		return
	}
	c.ll.Remove(el)
	delete(c.index, ent.id)
	c.curByt -= ent.bytes
	_ = os.Remove(c.path(ent.id))
}

// Remove is the public, id-keyed eviction entrypoint removeElement lacks: it
// drops the index entry, the LRU node, the byte count, AND the on-disk .seg file
// for a SPECIFIC id (a no-op when the id is absent). The deterministic rebuild's
// Manager.InvalidateLocal calls it per superseded id so a .seg file the server
// pruned (reconcilePrune) is evicted locally rather than orphaning until LRU —
// which never fires on an unbounded cache. Reuses removeElement verbatim.
func (c *diskSegmentCache) Remove(id searchengine.SegmentID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[id]; ok {
		c.removeElement(el)
	}
}

// Keys enumerates the content-hash ids of every segment currently in the live
// in-memory index — the L2-resident set — as a freshly-allocated slice. It is the
// server-independent enumeration the load() fallback uses to reconstruct the
// resident set from L2 alone when the server manifest is unavailable.
//
// It ranges the in-memory index (already populated at construction by
// scanExisting and kept current by Put/Remove); it does NOT re-read the disk, and
// it is recency-neutral — it does NOT MoveToFront, so enumerating the set never
// perturbs the LRU ordering. Safe to call concurrently with Get/Put/Remove under
// the same mutex.
func (c *diskSegmentCache) Keys() []searchengine.SegmentID {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]searchengine.SegmentID, 0, len(c.index))
	for id := range c.index {
		keys = append(keys, id)
	}
	return keys
}

// sizeOf reports the stored byte size of one cached id, and whether the id is
// resident at all.
//
// It reads the SAME in-memory accounting the eviction budget is kept in, so a
// caller sizing a copy before making it measures exactly what that copy will
// charge against the destination's budget — and it costs no disk read. Like
// Keys, it is recency-neutral: sizing a set must not reorder the LRU, or asking
// how big something is would change what gets evicted next.
func (c *diskSegmentCache) sizeOf(id searchengine.SegmentID) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[id]
	if !ok {
		return 0, false
	}
	entry, ok := el.Value.(*cacheEntry)
	if !ok {
		return 0, false
	}
	return entry.bytes, true
}

// atomicWriteFile writes b to path via a temp file + rename. This rolls its OWN
// temp+rename because the server's store.atomicWriteFile helper lives in a
// SEPARATE module and cannot be imported (the (path, bytes) shape also differs
// from the server's callback shape). Best-effort fsync of the temp before rename
// for crash durability.
func atomicWriteFile(path string, b []byte) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // path derives from a content-hash id under a fixed cache root
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
