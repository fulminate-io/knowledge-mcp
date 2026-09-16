// SPDX-License-Identifier: Apache-2.0

// release_input_classes_test.go drives the input classes the seal-path release
// meets that its own failure arms do not: a re-seal of byte-identical documents,
// a corpus whose partition count doubles under it, and the outcome tally the
// operator-facing release line is read from.

package segmentdist

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestReSealOfIdenticalBytesStaysMapped is R1.3's byte-identical re-seal class: a
// drain that seals a group whose documents have not changed mints the id the
// engine already holds, because a segment id IS its payload's content hash.
//
// THE RISK IT COVERS is the release re-arming on a segment it already released:
// the re-seal publishes nothing, so nothing becomes heap-backed, and the next
// durability pass must write nothing and map nothing. A release that keyed on "the
// seal ran" rather than on provenance would remap the whole pool on every drain.
func TestReSealOfIdenticalBytesStaysMapped(t *testing.T) {
	dm, ic := sealedPoolFixture(t)
	_, err := dm.persistResident()
	require.NoError(t, err)
	require.Empty(t, dm.engine.HeapBackedResidentIDs(), "control: the first pass released the seal")
	id := dm.engine.Export()[0].ID
	mapsAfterFirst := countOps(ic, "getmapped", id)

	// The same two documents again: the seal is idempotent by content hash and
	// publishes nothing.
	require.NoError(t, dm.engine.Add([]searchengine.Document{doc("a", "alpha"), doc("b", "beta")}))
	require.NoError(t, dm.engine.Flush())
	require.Equal(t, 1, dm.engine.ResidentSegmentCount(), "a re-seal of identical bytes must publish nothing new")
	require.Empty(t, dm.engine.HeapBackedResidentIDs(),
		"a re-seal that published nothing must leave no heap-backed payload behind")

	wrote, err := dm.persistResident()
	require.NoError(t, err)
	require.Equal(t, 0, wrote, "the bytes are already durable")
	require.Equal(t, mapsAfterFirst, countOps(ic, "getmapped", id),
		"a pool that is already mapping-backed must not be re-mapped by a re-seal")
	require.Equal(t, []searchengine.ExternalID{"a"}, searchHits(dm.engine, "alpha"))
}

// TestReleaseAfterAPartitionDoubling is R1.3's other undriven class: the batch
// spanning a partition doubling, where a group swap republishes the corpus under a
// new bucket count between the seal and the durability pass.
//
// The swap's outputs are merge-mapped and its inputs were released, so after the
// next persist nothing may be heap-backed and every document must still be
// reachable — the assertion that the release travels with a corpus whose partition
// layout changed under it.
func TestReleaseAfterAPartitionDoubling(t *testing.T) {
	dm, _ := sealedPoolFixture(t)
	_, err := dm.persistResident()
	require.NoError(t, err)
	require.Empty(t, dm.engine.HeapBackedResidentIDs(), "control: the seal was released before the doubling")

	const before, after = 2, 4
	constituents := []searchengine.SegmentID{dm.engine.Export()[0].ID}
	fresh := doc("c", "gamma")

	// Bucket the incoming document by the NEW count, which is what a doubling means:
	// the harvest's accept predicate keeps only the members of the partition it is
	// building, so a document placed by the old count is silently dropped.
	work := make([]searchengine.BucketWork, 0, after)
	for b := range after {
		w := searchengine.BucketWork{Bucket: b}
		if b == searchengine.BucketOf(fresh.ID, after) {
			w.Docs = []searchengine.Document{fresh}
		}
		work = append(work, w)
	}
	require.NotEqual(t, -1, searchengine.BucketOf(fresh.ID, before),
		"fixture control: the document has a partition under the old count too")

	published, _, err := dm.engine.ReplaceBucketGroup(t.Context(), after, constituents, work)
	require.NoError(t, err)
	require.NotEmpty(t, published, "the doubling must have published something")

	_, err = dm.persistResident()
	require.NoError(t, err)
	require.Empty(t, dm.engine.HeapBackedResidentIDs(),
		"after a partition doubling and its durability pass, nothing may still hold its encoder output")
	for _, tc := range []struct{ term, id string }{{"alpha", "a"}, {"beta", "b"}, {"gamma", "c"}} {
		require.Equal(t, []searchengine.ExternalID{tc.id}, searchHits(dm.engine, tc.term),
			"every document must survive the doubling")
	}
}

// TestTallyRemapOutcomesCountsEachArm pins the counting the operator-facing release
// line is read from, including the arm a manager test cannot produce on demand: a
// DECLINE reports no error and swapped nothing, and folding it into the release
// count is how "released=3738" gets printed for a batch that swapped none.
func TestTallyRemapOutcomesCountsEachArm(t *testing.T) {
	republished, declined, failed := tallyRemapOutcomes(map[searchengine.SegmentID]searchengine.RemapResult{
		"swapped":    {Outcome: searchengine.RemapRepublished},
		"swapped-2":  {Outcome: searchengine.RemapRepublished},
		"left-alone": {Outcome: searchengine.RemapDeclined},
		"undecodable": {
			Outcome: searchengine.RemapFailed,
			Err:     errors.New("remap segment undecodable: bad header"),
		},
	})
	require.Equal(t, 2, republished, "only a swap counts as a release")
	require.Equal(t, 1, declined, "a segment that left the set is a decline, not a release")
	require.Equal(t, 1, failed, "an undecodable blob is a failure, not a decline")

	// The empty batch: three zeros rather than a nil-map panic, because a pass whose
	// every id failed to map reaches this with nothing to tally.
	republished, declined, failed = tallyRemapOutcomes(nil)
	require.Zero(t, republished)
	require.Zero(t, declined)
	require.Zero(t, failed)
}
