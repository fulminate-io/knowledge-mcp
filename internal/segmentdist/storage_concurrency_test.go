// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// burstDestinations drives n distinct caller-named accounts through the root,
// which is what a burst of /mcp account headers does to the child map.
func burstDestinations(root *Manager, from, n int) {
	for i := range n {
		root.ForDestination(graphclient.WithDestination(context.Background(), burstAccount(from+i)))
	}
}

// TestStorageForRequestHoldSurvivesAConcurrentBurst is the bind-and-hold race.
//
// forRequest used to take its reference AFTER bindChild released the lock, so a
// concurrent burst could evict, Close and RemoveAll the very child it was about
// to return: the caller then served from a closed Manager whose cache directory
// no longer existed, and its read came back EMPTY with no error — the silent
// zero the whole keep-rule exists to prevent.
//
// The probe writes a document through the held child and reads it back INSIDE
// THE SAME HOLD. Within one hold the child cannot be evicted, so a zero here is
// the race and nothing else; between iterations the child is idle and may be
// evicted legitimately, which is why each iteration writes its own document
// before reading.
func TestStorageForRequestHoldSurvivesAConcurrentBurst(t *testing.T) {
	stubAccountSelection(t, "11111111-1111-4111-8111-111111111111")
	root := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	held := burstAccount(900)
	ctxHeld := graphclient.WithDestination(context.Background(), held)

	// CONTROL: the write-then-read-inside-one-hold cycle works with no burst at
	// all, so a failure below is the concurrency and not the fixture.
	child, release := root.forRequest(ctxHeld)
	require.NoError(t, child.ReplaceBucketFields(ctxHeld, kgtypes.GraphKnowledge, "default", nil,
		[]searchengine.Document{{ID: "held", Fields: map[string]string{searchengine.FieldContent: "heldword"}}}))
	hits, err := child.Search(ctxHeld, kgtypes.GraphKnowledge, "default", "heldword", nil, 10)
	require.NoError(t, err)
	require.Len(t, hits, 1, "CONTROL: a held child serves what it was just written")
	release()

	var empties, errs atomic.Int32
	var stop atomic.Bool
	var wg sync.WaitGroup

	wg.Go(func() {
		defer stop.Store(true)
		for range 60 {
			c, rel := root.forRequest(ctxHeld)
			writeErr := c.ReplaceBucketFields(ctxHeld, kgtypes.GraphKnowledge, "default", nil,
				[]searchengine.Document{{ID: "held", Fields: map[string]string{searchengine.FieldContent: "heldword"}}})
			got, searchErr := c.Search(ctxHeld, kgtypes.GraphKnowledge, "default", "heldword", nil, 10)
			rel()
			if writeErr != nil || searchErr != nil {
				errs.Add(1)
				continue
			}
			if len(got) != 1 {
				empties.Add(1)
			}
		}
	})
	for w := range 8 {
		wg.Go(func() {
			for n := 0; !stop.Load(); n++ {
				burstDestinations(root, w*10_000+n*40, 40)
			}
		})
	}
	wg.Wait()

	require.Zero(t, errs.Load(), "a held child must never fail its own write or read under a concurrent burst")
	require.Zero(t, empties.Load(),
		"a held child returned EMPTY for a document written inside the same hold — it was evicted and its cache directory deleted in the window between the bind and the reference")
}

// TestStorageEvictionVacatesTheCacheDirUnderTheLock is the stale-removal race,
// pinned at the invariant that closes it rather than by scheduling.
//
// A child's cache directory is sha256(storage \x00 account), so the child a
// REBIND creates for the same destination gets the SAME path. Deleting that
// path after the lock is dropped therefore reaches the rebind's own files, and
// RemoveAll reports success either way: the loss is silent. The eviction now
// RENAMES the directory under the lock that removed the map entry, so the
// canonical path is free the instant a rebind can see it and the deletion
// afterwards can only touch bytes nobody can reach.
//
// The scheduling-based version of this probe did NOT reproduce the race in this
// harness — a three-thousand-file removal still finishes after the rebind loop
// it was racing — so the instrument here is the invariant, observed by calling
// the bind directly and looking at the filesystem the moment it returns, before
// the caller's off-lock removal runs.
func TestStorageEvictionVacatesTheCacheDirUnderTheLock(t *testing.T) {
	stubAccountSelection(t, "11111111-1111-4111-8111-111111111111")
	cacheDir := t.TempDir()
	root := closeOnCleanup(t, NewManager(cacheDir, 0))

	victimDest := burstAccount(901)
	victim := root.ForDestination(graphclient.WithDestination(context.Background(), victimDest))
	require.NoError(t, os.MkdirAll(victim.cacheDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(victim.cacheDir, "old"), []byte("old"), 0o600))

	// Fill the map to the cap so the next bind must evict, and the victim is the
	// least recently used of them.
	burstDestinations(root, 0, graphclient.MaxBoundDestinations-1)

	incoming := burstAccount(902)
	_, evicted, release := root.bindChild(graphclient.WithDestination(context.Background(), incoming), incoming, false)
	release()

	require.Len(t, evicted, 1, "the bind must have evicted exactly one child to make room")
	require.Same(t, victim, evicted[0].mgr, "the least recently used child is the victim")
	require.NotEmpty(t, evicted[0].tombstone, "the victim's directory must be renamed, not left at the path a rebind will use")
	require.NoDirExists(t, victim.cacheDir,
		"the canonical path is still occupied when the bind returns: a rebind would land in a directory the pending removal is about to delete")
	require.FileExists(t, filepath.Join(evicted[0].tombstone, "old"), "the victim's bytes moved to the tombstone rather than vanishing")

	// A rebind now gets the canonical path back, and the removal the caller runs
	// afterwards cannot reach what it writes there.
	rebound := root.ForDestination(graphclient.WithDestination(context.Background(), victimDest))
	require.NoError(t, os.MkdirAll(rebound.cacheDir, 0o700))
	marker := filepath.Join(rebound.cacheDir, "rebound-marker")
	require.NoError(t, os.WriteFile(marker, []byte("rebound"), 0o600))
	for _, e := range evicted {
		require.NoError(t, os.RemoveAll(e.tombstone))
	}
	require.FileExists(t, marker, "the eviction's removal reached the rebound child's own files")
}
