// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// multiwriter_testkit_test.go is the SHARED fan-out kit for the client-side
// multi-writer correctness suite: K production Managers, each a DISTINCT
// restart-stable writer_id, all over ONE shared fake SegmentService. No existing
// helper stands up >1 Manager with distinct writer_ids against a shared server —
// every prior test is single-writer (or drives a bare distManager via
// buildManagerWithWriter). This kit factors that fan-out plus the per-writer
// manifest-WINDOW helpers the convergence proof rests on, reusing the existing
// fake (already manifest-aware, source_test.go) unchanged.

// fleetWriterID returns the distinct, restart-stable 16-lowercase-hex writer_id for
// fleet member n. The shape matches what readMachineIDCache validates (exactly 16
// hex), so a restarted member can re-use the SAME id by re-applying this value. n+1
// keeps member 0 off the all-zeros value generateWriterID uses as its rand-fail
// fallback, so no fleet id can be confused with that sentinel.
func fleetWriterID(n int) string {
	return fmt.Sprintf("%016x", n+1)
}

// newMultiWriterFleet stands up ONE shared fake SegmentService (via the existing
// newSegmentHarness — no new http server / fake is declared) and K production
// Managers over it, each with its OWN L2 cache dir and a DISTINCT restart-stable
// writer_id set directly on the unexported Manager.writerID field (in-package).
// NewManager would otherwise resolve the host machine-id for all K Managers,
// collapsing them into a single writer; setting the field is how the fleet earns
// distinct writer_ids on one host. Returns the K Managers and the shared fake so
// tests can read its manifests/blobs directly.
func newMultiWriterFleet(t *testing.T, k int) ([]*Manager, *fakeSegmentService) {
	t.Helper()
	svc, gc := newSegmentHarness(t)
	mgrs := make([]*Manager, k)
	for n := range mgrs {
		mgr := NewManager(gc, t.TempDir(), 0)
		mgr.writerID = fleetWriterID(n)
		mgrs[n] = mgr
	}
	return mgrs, svc
}

// restartFleetMember constructs a FRESH Manager that impersonates a restarted
// fleet member m: the SAME writer_id (fleetWriterID(n)) and the SAME L2 cache dir
// as the original, over the SAME shared caller. This is the restart shape the real
// daemon takes — a new process keeps its machine-id writer_id and re-uses its
// on-disk L2 — lifted to the Manager level. cacheDir must be the original member's
// dir (read it from mgr.cacheDir).
func restartFleetMember(t *testing.T, caller segmentCaller, n int, cacheDir string) *Manager {
	t.Helper()
	mgr := NewManager(caller, cacheDir, 0)
	mgr.writerID = fleetWriterID(n)
	return mgr
}

// writerManifest returns the published id-set the fake holds for one
// (graphKey, writer_id, format) — the per-writer manifest WINDOW the convergence
// proof reads to distinguish a genuine refcount from ship-idempotency. It reads the
// already-manifest-aware fake's svc.manifests[k][writerID\x00format] under the
// fake's lock; the graphKey k is derived via the fake's own key(target).
//
//nolint:unparam // format kept on the signature — hnsw vs bm25 is a real manifest dimension the window helper spans.
func writerManifest(svc *fakeSegmentService, target *knowledgev1.GraphSelector, writerID, format string) []string {
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
// manifest references yields 0 (it is GC-eligible). This is the discriminating
// signal: a no-op/broken refcount cannot make a dropped id survive via another
// writer.
func blobRefCount(svc *fakeSegmentService, target *knowledgev1.GraphSelector, id string) int {
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

// serverHasBlob reports whether the fake server still holds a blob with this id
// under target (any format) — the "did the refcount-GC reap it" signal.
func serverHasBlob(svc *fakeSegmentService, target *knowledgev1.GraphSelector, id string) bool {
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
// writer_ids, one shared fake, every writer's id observed after a ship, and the
// manifest-window helpers reading a real refcount-2 on a shared id.
func TestMultiWriterFleetSmoke(t *testing.T) {
	ctx := context.Background()
	const k = 3
	mgrs, svc := newMultiWriterFleet(t, k)

	// Distinct, restart-stable, 16-hex writer_ids.
	require.NotEqual(t, mgrs[0].writerID, mgrs[1].writerID, "fleet members get distinct writer_ids")
	for n, mgr := range mgrs {
		require.Equal(t, fleetWriterID(n), mgr.writerID, "writer_id is the restart-stable fleet shape")
		require.Len(t, mgr.writerID, 16, "writer_id is the 16-hex machine-id shape")
	}

	gt, name := kgtypes.GraphCode, "fleet-smoke"
	target := graphSelector(gt, name)

	// Two writers build the SAME deterministic corpus (hnswVecDocs is fixed-seed),
	// so both mint the SAME content-hash blob id X. Each publishes it as its own
	// manifest → blobRefCount(X) == 2 across two DISTINCT writer manifests.
	docs := hnswVecDocs(searchCorpusN)
	require.NoError(t, mgrs[0].AddAndShip(ctx, gt, name, docs))
	require.NoError(t, mgrs[1].AddAndShip(ctx, gt, name, docs))

	// Both writers' ids reached the server (the last-connection liveness wiring).
	svc.mu.Lock()
	require.True(t, svc.seenWriterIDs[mgrs[0].writerID], "writer0 id observed on the wire")
	require.True(t, svc.seenWriterIDs[mgrs[1].writerID], "writer1 id observed on the wire")
	svc.mu.Unlock()

	// The two writers minted the SAME blob id (content-hash idempotency on the
	// shared corpus), and the manifest window shows it referenced by BOTH.
	export := mgrs[0].managerFor(gt, name).engine.Export()
	require.Len(t, export, 1, "the deterministic corpus seals exactly one segment")
	sharedID := export[0].ID
	require.Equal(t, 2, blobRefCount(svc, target, sharedID),
		"a shared content-hash id published by two writers has refcount 2 — a real two-writer reference, not ship-idempotency")
	require.True(t, serverHasBlob(svc, target, sharedID), "the shared blob is present on the server")

	// The window helpers see each writer's manifest carry the shared id.
	require.Contains(t, writerManifest(svc, target, mgrs[0].writerID, "hnsw"), sharedID,
		"writer0's manifest references the shared id")
	require.Contains(t, writerManifest(svc, target, mgrs[1].writerID, "hnsw"), sharedID,
		"writer1's manifest references the shared id")
}
