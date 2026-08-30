// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rebuild_segments_tombstones_test.go covers the retention trim's MEMBERSHIP RULE:
// an id may leave the durable tombstone record only when the partition routing it
// has been re-emitted without it. The fixtures here exercise the trim directly
// rather than through the driver, because what is under test is which partition
// count the predicate is evaluated against — a property no driver-level assertion
// can see.

// tombstonePartitionFixture derives a trim fixture at a stated corpus partition
// count: the items a run emitted, and tombstoned ids whose partitions that run
// never touched. The ids are DERIVED through searchengine.BucketOf rather than
// hardcoded, so a fixture that drifts into a collision with an emitted partition
// fails loudly here instead of greening the assertion vacuously.
type tombstonePartitionFixture struct {
	items        []rebuildSegItem
	emitted      map[int]struct{}
	ids          []searchengine.ExternalID
	idPartitions []int
}

func newTombstonePartitionFixture(t *testing.T, corpusBucketCount int) tombstonePartitionFixture {
	t.Helper()

	f := tombstonePartitionFixture{emitted: map[int]struct{}{}}
	for i := 0; i < 4; i++ {
		nodeID := fmt.Sprintf("seg-node-%d", i)
		f.items = append(f.items, rebuildSegItem{nodeID: nodeID})
		f.emitted[searchengine.BucketOf(nodeID, corpusBucketCount)] = struct{}{}
	}

	// Two tombstoned ids in DISTINCT partitions, neither of them a partition this
	// run emitted. Scanned rather than hardcoded so the fixture states its own
	// requirement instead of assuming a hash.
	for i := 0; len(f.ids) < 2 && i < 4096; i++ {
		id := searchengine.ExternalID(fmt.Sprintf("deleted-id-%d", i))
		part := searchengine.BucketOf(id, corpusBucketCount)
		if _, touched := f.emitted[part]; touched {
			continue
		}
		if len(f.idPartitions) == 1 && f.idPartitions[0] == part {
			continue
		}
		f.ids = append(f.ids, id)
		f.idPartitions = append(f.idPartitions, part)
	}
	require.Len(t, f.ids, 2, "fixture could not derive two tombstoned ids in distinct un-emitted partitions")
	require.NotEqual(t, f.idPartitions[0], f.idPartitions[1], "the two tombstoned ids must span distinct partitions")

	return f
}

// TestRetainTombstonesKeepsIdsWhosePartitionTheRunNeverEmitted pins the trim to the
// partition count the re-emit ACTUALLY RAN AT rather than to the size of whatever
// the run happened to scan. A delta rebuild's window is a handful of items, and
// searchengine.BucketCountFor collapses any corpus of 1..DefaultMinSegmentDocs onto
// a single partition — under which every tombstoned id maps to bucket 0, the run's
// own items put bucket 0 in the emitted set, and the whole durable record is wiped.
func TestRetainTombstonesKeepsIdsWhosePartitionTheRunNeverEmitted(t *testing.T) {
	const corpusBucketCount = 128

	f := newTombstonePartitionFixture(t, corpusBucketCount)
	t.Logf("corpus partition count %d: run emitted partitions %v, tombstoned ids sit in partitions %v",
		corpusBucketCount, f.emitted, f.idPartitions)

	keep := retainTombstones(f.ids, f.items, corpusBucketCount)

	require.Len(t, keep, 2,
		"the trim dropped ids whose partitions this run never emitted: expected both of %v to survive, "+
			"got %v — a survivor count of zero is the window-derived bucket count collapsing every id onto partition 0",
		f.ids, keep)
	require.ElementsMatch(t, f.ids, keep)
}
