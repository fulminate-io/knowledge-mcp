// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// worker_embed_lease_test.go pins the EMBED axis's lease behaviour: one lease is
// N provider-cap calls and exactly ONE writeback.
//
// WHY THE COUNT OF Execute CALLS IS THE ASSERTION, and not the count of items
// written. Every shape writes all the items; what the convoy costs is the number
// of writeback TRANSACTIONS, because each one is one acquisition of the server's
// per-graph advisory write mutex (one Execute lowers to one store.Txn, whose
// first statement takes that lock). So the RPC count is the quantity under
// measurement and the item count is only the control that proves the work
// actually flowed.
//
// THE FIXTURE IS 500 ITEMS ON PURPOSE — five times the provider-call cap. A
// one-item or 100-item fixture would produce the same count under the old
// per-provider-call writeback and under the lease, and would prove nothing.

// leaseFixtureItems is the drive size: five provider calls' worth at the shipped
// embed provider cap, so the old and new shapes are distinguishable.
const leaseFixtureItems = 500

// execCallCount reads the fake's captured ExecuteRequest slice under its own
// mutex. The count of these IS the writeback count — writeBatchUpdates issues
// exactly one Execute per invocation.
func execCallCount(f *fakeWireClient) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.execRequests)
}

// embedLeaseFixture builds N EmbedWork items for ONE (graphType, graphName,
// backend) triple plus the matching canned vector map, so every item embeds
// successfully and reaches the writeback.
func embedLeaseFixture(n int) ([]EmbedWork, map[string][]byte) {
	work := make([]EmbedWork, 0, n)
	vectors := make(map[string][]byte, n)
	for i := range n {
		id := fmt.Sprintf("pkg/lease.go:Sym%d", i)
		work = append(work, embedWork(id, "embed text for "+id))
		vectors[id] = make([]byte, 32)
	}
	return work, vectors
}

// drainEmbedThroughDispatcher pushes work through the REAL dispatcher at the
// given batch size and runs each emitted batch through the REAL worker entry
// point, exactly as Pipeline.Start wires them. Returns when every batch has been
// processed, so a caller's assertions run against a quiesced fake.
func drainEmbedThroughDispatcher(ctx context.Context, p *Pipeline, work []EmbedWork, batchSize int) {
	in := make(chan EmbedWork, len(work))
	out := make(chan []EmbedWork, len(work)/max(batchSize, 1)+2)
	go runEmbedDispatcher(ctx, in, out, batchSize)
	for _, w := range work {
		in <- w
	}
	close(in)
	for batch := range out {
		runEmbedWorkerBatch(ctx, p, batch)
	}
}

// strideEmbedder records the SIZE of every embedder call in order and can fail a
// chosen call. fakeEmbedder keeps only the last call's items, and per-call sizes
// are exactly what the stride assertions are about, so this records them.
type strideEmbedder struct {
	mu       sync.Mutex
	sizes    []int
	vectors  map[string][]byte
	failCall int // 1-based index of the call that errors; 0 = none
	calls    int
}

func (s *strideEmbedder) call(_ context.Context, items []EmbedItem) (map[string][]byte, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.sizes = append(s.sizes, len(items))
	s.mu.Unlock()
	if n == s.failCall {
		return nil, errTerminalNonDeterministic
	}
	out := make(map[string][]byte, len(items))
	for _, it := range items {
		out[it.ID] = s.vectors[it.ID]
	}
	return out, nil
}

func (s *strideEmbedder) callSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.sizes...)
}

// TestEmbedLease_StridesAtProviderCap asserts the lease is spent as N calls at
// the PROVIDER-CALL cap rather than as one oversized call.
//
// The distinction is not cosmetic. Handing a whole lease to the embedder in one
// call routes it through the provider client's own packing loop, whose failure
// mode is to return on the first failing pack and discard every already-billed
// pack before it — so the stride is what keeps a lease's provider spend
// partitioned.
func TestEmbedLease_StridesAtProviderCap(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}
	lease := cfg.EmbedLeaseSizeOrDefault()
	cap := cfg.EmbedBatchSizeOrDefault()

	work, vectors := embedLeaseFixture(lease)
	wc := newFakeWireClient()
	se := &strideEmbedder{vectors: vectors}
	p := New(cfg, wc, nil, se.call)

	drainEmbedThroughDispatcher(ctx, p, work, lease)

	sizes := se.callSizes()
	wantCalls := (lease + cap - 1) / cap
	t.Logf("lease=%d provider_cap=%d call_sizes=%v writebacks=%d", lease, cap, sizes, execCallCount(wc))
	require.Len(t, sizes, wantCalls, "one lease must be spent as ceil(%d/%d) provider calls", lease, cap)
	for i, n := range sizes {
		require.LessOrEqual(t, n, cap, "provider call %d carried %d items, over the cap of %d", i+1, n, cap)
	}
	require.Equal(t, 1, execCallCount(wc), "those %d provider calls must still share ONE writeback", wantCalls)
}

// TestEmbedLease_FailedStrideLosesOnlyThatStride pins the stride-failure policy:
// a failed stride contributes nothing and the loop CONTINUES.
//
// This is the property that keeps the lease size from becoming a
// failure-amplification factor. The single writeback carries every succeeded
// stride; the failed stride's ids are absent from it, which leaves them
// embed-eligible for the next scan exactly as a dropped batch is today, and the
// unchanged error path has already stamped its own terminal marker for them.
func TestEmbedLease_FailedStrideLosesOnlyThatStride(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}
	lease := cfg.EmbedLeaseSizeOrDefault()
	cap := cfg.EmbedBatchSizeOrDefault()
	const failingCall = 3

	work, vectors := embedLeaseFixture(lease)
	wc := newFakeWireClient()
	se := &strideEmbedder{vectors: vectors, failCall: failingCall}
	p := New(cfg, wc, nil, se.call)

	drainEmbedThroughDispatcher(ctx, p, work, lease)

	require.Len(t, se.callSizes(), (lease+cap-1)/cap,
		"the loop must CONTINUE past the failed stride — a short call list means the lease aborted")

	// The ids of the failed stride, derived from the fixture rather than from
	// anything the run produced.
	failedIDs := map[string]bool{}
	for _, w := range work[(failingCall-1)*cap : failingCall*cap] {
		failedIDs[w.NodeID] = true
	}

	var vectorWrites, markerWrites []updateBatchItem
	for _, batch := range wc.recordedWrites {
		if len(batch) > 0 && len(batch[0].BinaryVector) > 0 {
			vectorWrites = append(vectorWrites, batch...)
			continue
		}
		markerWrites = append(markerWrites, batch...)
	}
	t.Logf("lease=%d failed_stride=%d vector_items=%d marker_items=%d writebacks=%d",
		lease, failingCall, len(vectorWrites), len(markerWrites), execCallCount(wc))

	// Compared against the FIXTURE-derived count, never against the length of
	// the other set — two sets that lost the same members stay equal.
	require.Len(t, vectorWrites, lease-cap, "the writeback must carry every succeeded stride and only those")
	for _, it := range vectorWrites {
		require.False(t, failedIDs[it.ID], "id %s came from the failed stride and must not be in the writeback", it.ID)
	}
	// The known positive on the other side: the failed stride was not silently
	// swallowed — the unchanged error path stamped its terminal markers.
	require.Len(t, markerWrites, cap, "the failed stride's ids must still receive their terminal markers")
	for _, it := range markerWrites {
		require.True(t, failedIDs[it.ID], "marker written for %s, which did not come from the failed stride", it.ID)
	}
}

// TestEmbedLease_OneWritebackPerLease is the RED-FIRST reproduction of the
// defect this ticket exists to remove: today the dispatcher's unit is the
// PROVIDER-CALL cap, so a 500-item drain of one graph costs five writeback
// transactions and five acquisitions of that graph's advisory write mutex.
//
// Against the lease-sized shape the same 500 items cost
// ceil(500 / EmbedLeaseSizeOrDefault()) writebacks — one at the shipped
// defaults. The expectation is DERIVED from the accessor rather than written as
// a literal, so it stays correct under a different worker count or provider cap
// instead of becoming a second, disagreeing authority on the lease size.
func TestEmbedLease_OneWritebackPerLease(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}
	lease := cfg.EmbedLeaseSizeOrDefault()
	require.Positive(t, lease, "the lease size must be a positive item count")

	work, vectors := embedLeaseFixture(leaseFixtureItems)
	wc := newFakeWireClient()
	fe := &fakeEmbedder{vectors: vectors}
	p := New(cfg, wc, nil, fe.call)

	drainEmbedThroughDispatcher(ctx, p, work, lease)

	wantWritebacks := (leaseFixtureItems + lease - 1) / lease
	t.Logf("lease=%d items=%d writebacks=%d provider_calls=%d",
		lease, leaseFixtureItems, execCallCount(wc), fe.calls.load())
	require.Equal(t, wantWritebacks, execCallCount(wc),
		"a %d-item drain of ONE graph must cost ceil(%d/%d)=%d writeback transactions — each one is an acquisition of that graph's advisory write mutex",
		leaseFixtureItems, leaseFixtureItems, lease, wantWritebacks)
	// The control: the reduced writeback count must not have come from losing
	// work. Compared against the fixture-derived constant, never against
	// another measurement of the same run.
	require.Equal(t, leaseFixtureItems, wc.totalWriteItems(),
		"every fixture item must still be written; a lower count means the lease dropped work rather than batching it")
}
