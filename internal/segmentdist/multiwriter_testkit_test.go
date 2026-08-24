// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// multiwriter_testkit_test.go is the SHARED fan-out kit for the client-side
// multi-writer correctness suite: K production Managers, each a DISTINCT
// restart-stable writer_id, all over ONE shared in-memory segment registry
// (sharedServerFake). Each member's Manager is wired to a per-writer VIEW of that
// shared server via withSegmentSource — the view stamps the member's writer_id into
// the shared server's manifests + seenWriterIDs on Ship/PublishManifest, which is
// the mechanism that REPLACES the deleted RPC ShipRequest.WriterId field (the
// per-writer identity now lives in the injected view, not on the wire).

// fleetMember is one writer in the fleet: an embedded production *Manager (so every
// member.AddAndMarkDirty / .managerFor / .ResidentDocCount / .cacheDir call site is
// unchanged), plus its distinct writer_id and its bound source view.
type fleetMember struct {
	*Manager
	writerID string
	view     *fakeSegmentSource
}

// newMultiWriterFleet stands up ONE shared server fake and K production Managers
// over it, each with its OWN L2 cache dir and a DISTINCT restart-stable writer_id.
// Each Manager is injected (withSegmentSource) with a per-writer view bound to the
// fleet target, so the K Managers earn distinct writer identities on one host
// without the deleted machine-id path. Returns the K members and the shared server
// so tests can read its manifests/blobs directly.
func newMultiWriterFleet(t *testing.T, k int) ([]*fleetMember, *sharedServerFake) {
	t.Helper()
	svc := newSharedServerFake()
	members := make([]*fleetMember, k)
	for n := range members {
		wid := fleetWriterID(n)
		view := svc.viewFor(&knowledgev1.GraphSelector{}, wid)
		mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(view)))
		members[n] = &fleetMember{Manager: mgr, writerID: wid, view: view}
	}
	return members, svc
}

// restartFleetMember constructs a FRESH Manager that impersonates a restarted fleet
// member n: the SAME writer_id (fleetWriterID(n)) and the SAME L2 cache dir as the
// original, over the SAME shared server. This is the restart shape the real daemon
// takes — a new process keeps its writer_id and re-uses its on-disk L2 — lifted to
// the Manager level. cacheDir must be the original member's dir (member.cacheDir).
func restartFleetMember(t *testing.T, svc *sharedServerFake, n int, cacheDir string) *fleetMember {
	t.Helper()
	wid := fleetWriterID(n)
	view := svc.viewFor(&knowledgev1.GraphSelector{}, wid)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, cacheDir, 0, withSegmentSource(view)))
	return &fleetMember{Manager: mgr, writerID: wid, view: view}
}

// writerManifest returns the published id-set the shared server holds for one
// (target, writer_id, format) — the per-writer manifest WINDOW the convergence proof
// reads to distinguish a genuine refcount from ship-idempotency.
//
//nolint:unparam // format kept on the signature — hnsw vs bm25 is a real manifest dimension the window helper spans.
func writerManifest(svc *sharedServerFake, target *knowledgev1.GraphSelector, writerID, format string) []string {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	k := svc.key(target)
	mk := writerID + "\x00" + format
	set := svc.manifests[k][mk]
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

// blobRefCount returns how many DISTINCT writer-manifest entries (writer\x00format
// keys) under target reference id — the reference count the server's mechanical
// refcount-GC keys on. A shared id published by two writers yields 2; an id no
// manifest references yields 0 (it is GC-eligible).
func blobRefCount(svc *sharedServerFake, target *knowledgev1.GraphSelector, id string) int {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	k := svc.key(target)
	count := 0
	for _, set := range svc.manifests[k] {
		if set[id] {
			count++
		}
	}
	return count
}

// serverHasBlob reports whether the shared server still holds a blob with this id
// under target (any format) — the "did the refcount-GC reap it" signal.
func serverHasBlob(svc *sharedServerFake, target *knowledgev1.GraphSelector, id string) bool {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	k := svc.key(target)
	for _, b := range svc.byKey[k] {
		if b.GetId() == id {
			return true
		}
	}
	return false
}

// TestMultiWriterFleetSmoke pins the fleet harness contract: distinct restart-stable
// writer_ids, one shared server, every writer's id observed after a ship, and the
// manifest-window helpers reading a real refcount-2 on a shared id.
func TestMultiWriterFleetSmoke(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const k = 3
	members, svc := newMultiWriterFleet(t, k)

	// Distinct, restart-stable, 16-hex writer_ids.
	require.NotEqual(t, members[0].writerID, members[1].writerID, "fleet members get distinct writer_ids")
	for n, member := range members {
		require.Equal(t, fleetWriterID(n), member.writerID, "writer_id is the restart-stable fleet shape")
		require.Len(t, member.writerID, 16, "writer_id is the 16-hex machine-id shape")
	}

	gt, name := kgtypes.GraphCode, "fleet-smoke"
	target := graphSelector(gt, name)
	// Bind each member's injected view to the fleet target so its ship/publish legs
	// record under the right target-key on the shared server.
	for _, member := range members {
		member.view.target = target
	}

	// Two writers build the SAME deterministic corpus (hnswVecDocs is fixed-seed),
	// so both mint the SAME content-hash blob id X. Each publishes it as its own
	// manifest → blobRefCount(X) == 2 across two DISTINCT writer manifests.
	// Half a threshold keeps the corpus inside a SINGLE partition through the tick
	// (the tick counts the incoming window alongside the resident set), so the shared
	// corpus still mints exactly one blob id for both writers to reference.
	docs := hnswVecDocs(searchCorpusN / 2)
	require.NoError(t, members[0].AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, members[0].ReEmitDirtyBuckets(ctx, gt, name))
	require.NoError(t, members[1].AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, members[1].ReEmitDirtyBuckets(ctx, gt, name))

	// Both writers' ids reached the server (the last-connection liveness wiring).
	svc.mu.Lock()
	require.True(t, svc.seenWriterIDs[members[0].writerID], "writer0 id observed on the wire")
	require.True(t, svc.seenWriterIDs[members[1].writerID], "writer1 id observed on the wire")
	svc.mu.Unlock()

	// The two writers minted the SAME blob id (content-hash idempotency on the
	// shared corpus), and the manifest window shows it referenced by BOTH.
	export := members[0].managerFor(gt, name).engine.Export()
	require.Len(t, export, 1, "the deterministic corpus re-emits as exactly one partition")
	sharedID := export[0].ID
	require.Equal(t, 2, blobRefCount(svc, target, sharedID),
		"a shared content-hash id published by two writers has refcount 2 — a real two-writer reference, not ship-idempotency")
	require.True(t, serverHasBlob(svc, target, sharedID), "the shared blob is present on the server")

	// The window helpers see each writer's manifest carry the shared id.
	require.Contains(t, writerManifest(svc, target, members[0].writerID, hnsw.New().Name()), sharedID,
		"writer0's manifest references the shared id")
	require.Contains(t, writerManifest(svc, target, members[1].writerID, hnsw.New().Name()), sharedID,
		"writer1's manifest references the shared id")
}
