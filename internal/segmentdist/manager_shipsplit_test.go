// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// shipPartitionSanityBound is the test-side tripwire for the partitioned Ship
// request bodies. 64 MiB (MaxCloudRequestBytes) is a PARTITION TARGET, not a hard
// ceiling: the packer accumulates whole blobs until the next would cross the
// budget, so a Ship request can land SLIGHTLY over 64 MiB (a lone blob at/over
// the budget gets its own sub-batch; the envelope adds overhead). What must NEVER
// happen is a request approaching Cloudflare's ~100 MiB edge cap — so the
// tripwire is a sanity bound comfortably under that (~90 MiB), not a strict
// ≤64 MiB cap. With ~1 MiB test blobs no request gets anywhere near this.
const shipPartitionSanityBound = 90 << 20 // 94371840 bytes — well under Cloudflare's ~100 MiB cap

// TestShipNewPartitionsOversizedDiff drives shipNew with an unshipped diff whose
// blobs sum to well over the 64 MiB partition target and asserts:
//
//	(a) more than one Ship RPC fired (the diff was partitioned into sub-batches);
//	(b) every recorded per-Ship proto.Size stays within the partition sanity bound
//	    (~90 MiB, well under Cloudflare's ~100 MiB cap) — the must-never-fire
//	    tripwire as a partition-range guard rather than a strict ≤64 MiB cap;
//	(c) the sharedServerFake's byKey holds EVERY input blob id after the ship (full
//	    reassembly server-side — none dropped, regardless of input size);
//	(d) shippedIDs/locallyShipped and the L2 cache are warmed for every blob
//	    (the per-response side-effects survive across the sub-batches).
//
// The diff is synthesized directly (the same shape ship() builds) so the blob
// byte sizes are controlled precisely without materializing 64 MiB of mock docs;
// the manager + sharedServerFake harness is the real ship round-trip.
func TestShipNewPartitionsOversizedDiff(t *testing.T) {
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "shipsplit"}
	ctx := context.Background()

	mgr, cc := buildManager(newMockEngine(t), gc, target, t.TempDir())

	// 70 blobs, each ~1 MiB → ~70 MiB total, well over the 64 MiB partition target
	// so the diff must partition into multiple Ship RPCs in the ~64 MiB range.
	const numBlobs = 70
	const blobBytes = 1 << 20
	payload := []byte(strings.Repeat("x", blobBytes))

	var diff []*knowledgev1.SegmentBlobProto
	diffBlobs := make(map[string]searchengine.SegmentBlob)
	// searchengine.SegmentID is an alias for string, so ids stay plain strings.
	wantIDs := make([]string, 0, numBlobs)
	for i := range numBlobs {
		id := "seg-" + strings.Repeat("0", 3) + "-" + itoaShip(i)
		b := searchengine.SegmentBlob{ID: id, Format: "mock", Bytes: payload, DocCount: 1}
		diff = append(diff, blobToProto(b))
		diffBlobs[id] = b
		wantIDs = append(wantIDs, id)
	}

	require.NoError(t, mgr.shipNew(ctx, diff, diffBlobs))

	// (a) the oversized diff was partitioned into more than one Ship RPC.
	require.Greater(t, cc.shipCalls.Load(), int64(1), "an oversized diff must be partitioned into multiple Ship RPCs")
	require.Equal(t, int64(numBlobs), cc.shipBlobs.Load(), "every blob must be shipped across the sub-batches")

	// (b) every recorded per-Ship request body stays within the partition sanity
	// bound (~90 MiB), comfortably under Cloudflare's ~100 MiB edge cap. 64 MiB is
	// the partition TARGET, so a request landing slightly over 64 MiB is fine; the
	// tripwire is this sanity bound, not a strict ≤64 MiB cap.
	for i, size := range cc.recordedShipBytes() {
		assert.Lessf(t, size, shipPartitionSanityBound,
			"Ship RPC %d proto.Size %d must stay within the partition sanity bound %d (the must-never-fire tripwire)",
			i+1, size, shipPartitionSanityBound)
	}

	// (c) full reassembly server-side: the sharedServerFake's byKey holds every id.
	svc.mu.Lock()
	stored := map[string]bool{}
	for _, b := range svc.byKey[svc.key(target)] {
		stored[b.GetId()] = true
	}
	svc.mu.Unlock()
	require.Len(t, stored, numBlobs, "the server must hold every shipped blob (none dropped)")
	for _, id := range wantIDs {
		assert.Truef(t, stored[id], "server is missing blob %s", id)
	}

	// (d) bookkeeping + L2 cache warmed for every blob across all sub-batches.
	mgr.shipMu.Lock()
	shippedCount := len(mgr.shippedIDs)
	locallyCount := len(mgr.locallyShipped)
	mgr.shipMu.Unlock()
	require.Equal(t, numBlobs, shippedCount, "shippedIDs must hold every blob across sub-batches")
	require.Equal(t, numBlobs, locallyCount, "locallyShipped must hold every blob across sub-batches")
	for _, id := range wantIDs {
		_, ok := mgr.cache.Get(id)
		assert.Truef(t, ok, "L2 cache must be warm for blob %s", id)
	}
}

func itoaShip(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
