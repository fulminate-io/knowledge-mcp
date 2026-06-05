// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDiskSegmentCachePutGet covers Put then Get returns bytes+true; an absent
// id returns (nil,false); and a fresh cache over the same dir Gets a prior id
// (restart re-load from disk).
func TestDiskSegmentCachePutGet(t *testing.T) {
	dir := t.TempDir()
	c := newDiskSegmentCache(dir, 0) // unbounded

	c.Put("abc", []byte("hello"))
	got, ok := c.Get("abc")
	require.True(t, ok)
	require.Equal(t, []byte("hello"), got)

	_, ok = c.Get("missing")
	require.False(t, ok)

	// Fresh cache over the SAME dir recovers the prior id from disk.
	c2 := newDiskSegmentCache(dir, 0)
	got2, ok := c2.Get("abc")
	require.True(t, ok)
	require.Equal(t, []byte("hello"), got2)
}

// TestDiskSegmentCacheLRUEviction verifies Put past the byte cap evicts the
// least-recently-used entry and keeps the most-recently-used.
func TestDiskSegmentCacheLRUEviction(t *testing.T) {
	dir := t.TempDir()
	// Cap at 20 bytes; each blob is 10 bytes → at most 2 resident.
	c := newDiskSegmentCache(dir, 20)

	c.Put("a", make([]byte, 10))
	c.Put("b", make([]byte, 10))
	// Touch "a" so "b" becomes the LRU.
	_, ok := c.Get("a")
	require.True(t, ok)

	// Put "c" (10 bytes) → total would be 30 > 20 → evict LRU ("b").
	c.Put("c", make([]byte, 10))

	_, ok = c.Get("b")
	require.False(t, ok, "LRU entry b should be evicted")
	_, ok = c.Get("a")
	require.True(t, ok, "recently-used a should survive")
	_, ok = c.Get("c")
	require.True(t, ok, "just-inserted MRU c should survive")

	// Total resident bytes under cap.
	require.LessOrEqual(t, c.curByt, int64(20))
}

// TestDiskSegmentCacheRemove verifies the public id-keyed Remove drops the entry
// AND the on-disk .seg file, so a superseded id the rebuild's InvalidateLocal
// passes is evicted explicitly (not left to orphan until LRU). A Remove of an
// absent id is a no-op.
func TestDiskSegmentCacheRemove(t *testing.T) {
	dir := t.TempDir()
	c := newDiskSegmentCache(dir, 0) // unbounded — LRU never fires, so Remove is the only eviction

	c.Put("seg1", []byte("vector-blob"))
	_, ok := c.Get("seg1")
	require.True(t, ok, "blob present after Put")
	require.FileExists(t, c.path("seg1"))

	c.Remove("seg1")

	_, ok = c.Get("seg1")
	require.False(t, ok, "Get must miss after Remove")
	_, statErr := os.Stat(c.path("seg1"))
	require.True(t, os.IsNotExist(statErr), "the .seg file must be deleted from disk")
	require.Equal(t, int64(0), c.curByt, "byte counter decremented on Remove")

	// Remove of an absent id is a harmless no-op.
	require.NotPanics(t, func() { c.Remove("never-existed") })
}
