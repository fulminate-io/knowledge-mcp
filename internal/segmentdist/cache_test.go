// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestDiskSegmentCachePutGet covers Put then Get returns bytes+true; an absent
// id returns (nil,false); and a fresh cache over the same dir Gets a prior id
// (restart re-load from disk).
func TestDiskSegmentCachePutGet(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	c := newDiskSegmentCache(dir, 0, adviceRandom) // unbounded

	// THE IDS ARE REAL CONTENT HASHES. Put verifies that the bytes hash to the
	// id it is given, so a placeholder like "abc" is now refused — correctly: it
	// is not an address of anything in a content-addressed store.
	payload := []byte("hello")
	id := sha256Hex(payload)

	require.NoError(t, c.Put(id, payload))
	got, ok := c.Get(id)
	require.True(t, ok)
	require.Equal(t, payload, got)

	_, ok = c.Get(sha256Hex([]byte("missing")))
	require.False(t, ok)

	// Fresh cache over the SAME dir recovers the prior id from disk.
	c2 := newDiskSegmentCache(dir, 0, adviceRandom)
	got2, ok := c2.Get(id)
	require.True(t, ok)
	require.Equal(t, payload, got2)
}

// TestDiskSegmentCacheLRUEviction verifies Put past the byte cap evicts the
// least-recently-used entry and keeps the most-recently-used.
func TestDiskSegmentCacheLRUEviction(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Cap at 20 bytes; each blob is 10 bytes → at most 2 resident.
	c := newDiskSegmentCache(dir, 20, adviceRandom)

	// DISTINCT payloads, because the id is now the content hash: three copies of
	// the same ten zero bytes would be ONE id, and the eviction this test is
	// about would never be exercised.
	blobA := bytes.Repeat([]byte("a"), 10)
	blobB := bytes.Repeat([]byte("b"), 10)
	blobC := bytes.Repeat([]byte("c"), 10)
	idA, idB, idC := sha256Hex(blobA), sha256Hex(blobB), sha256Hex(blobC)

	require.NoError(t, c.Put(idA, blobA))
	require.NoError(t, c.Put(idB, blobB))
	// Touch A so B becomes the LRU.
	_, ok := c.Get(idA)
	require.True(t, ok)

	// Put C (10 bytes) → total would be 30 > 20 → evict LRU (B).
	require.NoError(t, c.Put(idC, blobC))

	_, ok = c.Get(idB)
	require.False(t, ok, "LRU entry b should be evicted")
	_, ok = c.Get(idA)
	require.True(t, ok, "recently-used a should survive")
	_, ok = c.Get(idC)
	require.True(t, ok, "just-inserted MRU c should survive")

	// Total resident bytes under cap.
	require.LessOrEqual(t, c.curByt, int64(20))
}

// TestDiskSegmentCacheRemove verifies the public id-keyed Remove drops the entry
// AND the on-disk .seg file, so a superseded id the rebuild's InvalidateLocal
// passes is evicted explicitly (not left to orphan until LRU). A Remove of an
// absent id is a no-op.
func TestDiskSegmentCacheRemove(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	c := newDiskSegmentCache(dir, 0, adviceRandom) // unbounded — LRU never fires, so Remove is the only eviction

	blob := []byte("vector-blob")
	seg1 := sha256Hex(blob)

	require.NoError(t, c.Put(seg1, blob))
	_, ok := c.Get(seg1)
	require.True(t, ok, "blob present after Put")
	require.FileExists(t, c.path(seg1))

	c.Remove(seg1)

	_, ok = c.Get(seg1)
	require.False(t, ok, "Get must miss after Remove")
	_, statErr := os.Stat(c.path(seg1))
	require.True(t, os.IsNotExist(statErr), "the .seg file must be deleted from disk")
	require.Equal(t, int64(0), c.curByt, "byte counter decremented on Remove")

	// Remove of an absent id is a harmless no-op.
	require.NotPanics(t, func() { c.Remove(sha256Hex([]byte("never-existed"))) })
}

// TestDiskSegmentCacheKeys verifies Keys() enumerates exactly the live in-memory
// index: a fresh cache over a dir with N .seg files returns those N ids
// (set-equal, recovered by scanExisting); a Removed id drops from Keys() and a
// Put id appears — proving Keys() tracks the live index, not a one-shot disk scan.
func TestDiskSegmentCacheKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	seed := newDiskSegmentCache(dir, 0, adviceRandom)
	blobA, blobB, blobC, blobD := []byte("blob-a"), []byte("blob-b"), []byte("blob-c"), []byte("blob-d")
	segA, segB, segC, segD := sha256Hex(blobA), sha256Hex(blobB), sha256Hex(blobC), sha256Hex(blobD)
	require.NoError(t, seed.Put(segA, blobA))
	require.NoError(t, seed.Put(segB, blobB))
	require.NoError(t, seed.Put(segC, blobC))

	// A fresh cache over the SAME dir recovers membership via scanExisting; Keys()
	// returns exactly those three ids (order-independent set equality).
	c := newDiskSegmentCache(dir, 0, adviceRandom)
	require.ElementsMatch(t, []searchengine.SegmentID{segA, segB, segC}, c.Keys())

	// A Removed id drops from Keys().
	c.Remove(segB)
	require.ElementsMatch(t, []searchengine.SegmentID{segA, segC}, c.Keys())

	// A Put id appears in Keys().
	require.NoError(t, c.Put(segD, blobD))
	require.ElementsMatch(t, []searchengine.SegmentID{segA, segC, segD}, c.Keys())

	// An empty cache enumerates to an empty (non-nil) slice.
	empty := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
	require.Empty(t, empty.Keys())
}
