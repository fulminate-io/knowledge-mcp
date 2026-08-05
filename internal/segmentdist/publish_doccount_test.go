// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// recordingSource is a segmentSource that records the digests passed to
// PublishManifest and returns a caller-configured List (so the publish coverage
// gate's subset check passes). It is used to assert publishResident threads the
// real per-digest DocCount to the wire.
type recordingSource struct {
	listMetas          []searchengine.SegmentMeta
	published          []segmentDigest
	publishCalls       int
	verifiesServerSide bool
	publishErr         error // when set, PublishManifest returns it
}

func (s *recordingSource) List(_ context.Context, _ uint64) ([]searchengine.SegmentMeta, error) {
	return s.listMetas, nil
}

func (s *recordingSource) Fetch(_ context.Context, _ []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	return nil, nil
}

func (s *recordingSource) Ship(_ context.Context, _ []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error) {
	return nil, nil
}

func (s *recordingSource) Prune(_ []searchengine.SegmentID) (int, error) { return 0, nil }

func (s *recordingSource) PublishManifest(_ string, digests []segmentDigest) (int, error) {
	s.publishCalls++
	s.published = append([]segmentDigest(nil), digests...)
	return 0, s.publishErr
}

// verifiesServerSide toggles the recording source's completeness-authority signal so
// tests can exercise both the subset-checked (rpc/local) and subset-skipped (gcs)
// publish-gate branches.
func (s *recordingSource) verifiesCompletenessServerSide() bool { return s.verifiesServerSide }

// TestPublishResidentThreadsDocCount proves publishResident calls
// source.PublishManifest with segmentDigests whose DocCount equals each resident
// segment's Export DocCount — non-zero, and per blob. (A second leg used to cover
// sibling-engine digests carried alongside them; the manifest is this engine's resident
// set alone now.) A tiny corpus (summed doc count < residentBackstopFloor)
// disarms the coverage ratio; the recording source's List returns the live ids so
// the subset gate passes — isolating the doc_count-threading behavior under test.
func TestPublishResidentThreadsDocCount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "doccount"}

	// Resident segments (from Export) carry known doc counts. A third digest used to
	// ride alongside them as a sibling-engine reference; there is one engine per format
	// now, so the manifest is the resident set alone and the threading under test is
	// per-resident-blob.
	all := []searchengine.SegmentBlob{
		{ID: "seg-a", Format: "hnsw", DocCount: 7},
		{ID: "seg-b", Format: "hnsw", DocCount: 9},
	}

	rec := &recordingSource{
		// List returns the full live id-set so liveSetSubsetOfList0 passes; the summed
		// doc count (7+9=16) is under residentBackstopFloor=64, disarming the ratio.
		listMetas: []searchengine.SegmentMeta{
			{ID: "seg-a", Format: "hnsw", DocCount: 7},
			{ID: "seg-b", Format: "hnsw", DocCount: 9},
		},
	}
	cache := newDiskSegmentCache(t.TempDir(), 0)
	dm := newDistManager[mockQuery, mockStats](newMockEngine(), rec, cache, target, "hnsw")

	dropped, err := dm.publishResident(ctx, all, dm.locallyShipped)
	require.NoError(t, err)
	require.Nil(t, dropped, "no reconcileAgainst ids drop out on a first publish")

	require.Equal(t, 1, rec.publishCalls, "publishResident issues exactly one PublishManifest")
	got := map[searchengine.SegmentID]int{}
	for _, d := range rec.published {
		got[d.ID] = d.DocCount
	}
	require.Equal(t, map[searchengine.SegmentID]int{"seg-a": 7, "seg-b": 9}, got,
		"each published digest carries the resident Export DocCount — not 0")
}

// TestVerifiesCompletenessServerSide pins the capability flag on the surviving
// impls: gcs→true, local→false, and the errorSegmentSource sentinel→false (the
// three-implementer discipline — a missed impl is caught here, not left to the
// compile gate). The deleted rpc source's →false leg is gone with the RPC path.
func TestVerifiesCompletenessServerSide(t *testing.T) {
	t.Parallel()

	cache := newDiskSegmentCache(t.TempDir(), 0)

	gcs := newGCSSegmentSource(&fakeSegmentBackend{}, "code", "repo", "hnsw")
	require.True(t, gcs.verifiesCompletenessServerSide(), "gcs source verifies via the agent HEAD-verify")

	local := newLocalSegmentSource(cache, "hnsw")
	require.False(t, local.verifiesCompletenessServerSide(), "local source has no server verify")

	sentinel := &errorSegmentSource{reason: errNilSegmentTransport}
	require.False(t, sentinel.verifiesCompletenessServerSide(), "the fail-loud sentinel reports no server verify (bool, not error)")
}

// TestPublishGateSourceAware proves the subset-completeness check is SKIPPED for a
// completeness-server-side (GCS-like) source and RETAINED for an rpc-like source. The
// prior manifest is EMPTY (List returns nothing), so a non-empty live set is NOT a
// subset of List(0): the rpc-like source SKIPS the publish (deadlock the plan warns
// of), while the GCS-like source publishes it.
func TestPublishGateSourceAware(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "gate"}
	all := []searchengine.SegmentBlob{{ID: "seg-a", Format: "hnsw", DocCount: 3}}

	// rpc-like: verifiesServerSide=false, empty List → live NOT subset → SKIP (no publish).
	rpcLike := &recordingSource{verifiesServerSide: false} // listMetas nil == empty List(0)
	rpcDM := newDistManager[mockQuery, mockStats](newMockEngine(), rpcLike, newDiskSegmentCache(t.TempDir(), 0), target, "hnsw")
	dropped, err := rpcDM.publishResident(ctx, all, rpcDM.locallyShipped)
	require.NoError(t, err)
	require.Nil(t, dropped)
	require.Equal(t, 0, rpcLike.publishCalls, "rpc-like source SKIPS the publish (live set not a subset of the empty List(0))")

	// gcs-like: verifiesServerSide=true → subset check skipped → FIRST publish writes.
	gcsLike := &recordingSource{verifiesServerSide: true}
	gcsDM := newDistManager[mockQuery, mockStats](newMockEngine(), gcsLike, newDiskSegmentCache(t.TempDir(), 0), target, "hnsw")
	_, err = gcsDM.publishResident(ctx, all, gcsDM.locallyShipped)
	require.NoError(t, err)
	require.Equal(t, 1, gcsLike.publishCalls, "gcs-like source publishes the first (empty-prior) manifest — not skipped")
	require.Len(t, gcsLike.published, 1)
	require.Equal(t, "seg-a", gcsLike.published[0].ID)

	// SECOND publish adds a new segment → included in the manifest.
	all2 := append(all, searchengine.SegmentBlob{ID: "seg-b", Format: "hnsw", DocCount: 4})
	_, err = gcsDM.publishResident(ctx, all2, gcsDM.locallyShipped)
	require.NoError(t, err)
	require.Equal(t, 2, gcsLike.publishCalls)
	got := map[searchengine.SegmentID]int{}
	for _, d := range gcsLike.published {
		got[d.ID] = d.DocCount
	}
	require.Equal(t, map[searchengine.SegmentID]int{"seg-a": 3, "seg-b": 4}, got, "the add-publish includes the new segment")
}

// TestPublishResidentSkipsOnIncomplete proves a PublishManifest manifestIncompleteError
// (the agent 409'd on a missing blob) is a LOGGED SKIP inside publishResident: it
// returns (nil, nil) — no error, no bookkeeping reconcile — leaving the prior manifest
// intact.
func TestPublishResidentSkipsOnIncomplete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "incomplete"}
	all := []searchengine.SegmentBlob{{ID: "seg-a", Format: "hnsw", DocCount: 3}}

	src := &recordingSource{
		verifiesServerSide: true,
		publishErr:         &manifestIncompleteError{Missing: []string{"seg-a"}},
	}
	dm := newDistManager[mockQuery, mockStats](newMockEngine(), src, newDiskSegmentCache(t.TempDir(), 0), target, "hnsw")
	dropped, err := dm.publishResident(ctx, all, dm.locallyShipped)
	require.NoError(t, err, "an incomplete-publish 409 is a logged SKIP, not a hard error")
	require.Nil(t, dropped, "a skipped publish reconciles nothing")
	require.Equal(t, 1, src.publishCalls, "the publish was attempted (and skipped on the 409)")
}

// TestPublishResidentUnstampsMissingOn409 is the CONVERGENCE fix: when the
// agent 409s reporting a referenced blob genuinely absent server-side, publishResident
// UN-STAMPS the reported-missing ids from BOTH shippedIDs and locallyShipped, so
// shipAndPublish's ship diff (which skips every already-stamped blob) re-uploads them on
// the next tick. Before the fix the ids stayed stamped, the diff skipped them forever,
// and the 409 wedged the manifest permanently.
func TestPublishResidentUnstampsMissingOn409(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "unstamp"}
	all := []searchengine.SegmentBlob{
		{ID: "seg-a", Format: "hnsw", DocCount: 3},
		{ID: "seg-b", Format: "hnsw", DocCount: 4},
	}

	// The agent reports seg-a absent (the stamped-but-absent blob); seg-b is present.
	src := &recordingSource{
		verifiesServerSide: true,
		publishErr:         &manifestIncompleteError{Missing: []string{"seg-a"}},
	}
	dm := newDistManager[mockQuery, mockStats](newMockEngine(), src, newDiskSegmentCache(t.TempDir(), 0), target, "hnsw")

	// Pre-409 bookkeeping: both blobs are stamped as shipped (the ship RPC returned, so
	// the client believes the server holds them) in BOTH views.
	for _, id := range []searchengine.SegmentID{"seg-a", "seg-b"} {
		dm.shippedIDs[id] = struct{}{}
		dm.locallyShipped[id] = struct{}{}
	}

	dropped, err := dm.publishResident(ctx, all, dm.locallyShipped)
	require.NoError(t, err, "a 409 is a logged skip, not a hard error")
	require.Nil(t, dropped, "a skipped publish reconciles nothing")

	// THE FIX: the reported-missing id is un-stamped from BOTH views so the next ship
	// diff re-uploads it; the id the agent did NOT report missing is left stamped.
	require.NotContains(t, dm.shippedIDs, searchengine.SegmentID("seg-a"),
		"the missing blob is un-stamped from shippedIDs so shipAndPublish's diff re-uploads it")
	require.NotContains(t, dm.locallyShipped, searchengine.SegmentID("seg-a"),
		"the missing blob is un-stamped from locallyShipped too (symmetric with shipNew re-stamping both)")
	require.Contains(t, dm.shippedIDs, searchengine.SegmentID("seg-b"),
		"a blob the agent did NOT report missing stays stamped — only the missing ids re-upload")
	require.Contains(t, dm.locallyShipped, searchengine.SegmentID("seg-b"),
		"the non-missing blob stays in locallyShipped too")

	// The retry bit is armed so a later tick re-attempts ship+publish.
	require.True(t, dm.publishRetryPending(), "the 409 armed the publish retry")
}
