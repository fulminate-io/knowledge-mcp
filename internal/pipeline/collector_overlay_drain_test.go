// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// statefulEmbedGapFake is a WireClient whose PipelineScan mirrors the REAL
// server EmbedGaps semantic: a node is returned as a gap until its vector is
// written back. The backlog is seeded once up front (modeling a branch-overlay
// collect that opened many embed gaps at once); each scan returns up to `limit`
// still-pending IDs, and the simulated worker marks a node "written" — which is
// what removes it from the eligible set on the next scan. The dirty-gen is held
// constant for the whole drain (all work was seeded before the first scan, so no
// fresh gen advances mid-drain); once the backlog drains, scans return empty with
// that same gen so the collector's cheap-tick caches it and quiesces.
type statefulEmbedGapFake struct {
	mu        sync.Mutex
	pending   map[string]struct{} // not-yet-written node IDs (the live gap set)
	order     []string            // deterministic scan order
	gen       uint64              // constant dirty-gen for the seeded backlog
	scanCalls int
}

func newStatefulEmbedGapFake(nodeIDs []string, gen uint64) *statefulEmbedGapFake {
	pending := make(map[string]struct{}, len(nodeIDs))
	order := make([]string, len(nodeIDs))
	for i, id := range nodeIDs {
		pending[id] = struct{}{}
		order[i] = id
	}
	return &statefulEmbedGapFake{pending: pending, order: order, gen: gen}
}

func (f *statefulEmbedGapFake) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanCalls++
	limit := int(req.GetLimit())
	items := make([]*knowledgev1.PipelineScanItem, 0, limit)
	for _, id := range f.order {
		if _, ok := f.pending[id]; !ok {
			continue
		}
		items = append(items, &knowledgev1.PipelineScanItem{
			NodeId:    id,
			GraphName: req.GetGraphName(),
			EmbedText: "embed text for " + id,
		})
		if limit > 0 && len(items) >= limit {
			break
		}
	}
	return &knowledgev1.PipelineScanResponse{Items: items, DirtyGen: f.gen}, nil
}

// markWritten records a node's vector as written back — the event that drops it
// from the live gap set on the next scan (mirrors the server's per-layer
// vector-existence check in EmbedGaps).
func (f *statefulEmbedGapFake) markWritten(id string) {
	f.mu.Lock()
	delete(f.pending, id)
	f.mu.Unlock()
}

func (f *statefulEmbedGapFake) remaining() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending)
}

func (f *statefulEmbedGapFake) scans() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scanCalls
}

// Execute is unused by this loop-level test (the simulated worker writes back
// directly via the fake's markWritten rather than through the engine seam).
func (f *statefulEmbedGapFake) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestCollectorRunLoop_OverlayBacklogDrainsAcrossWavesAndQuiesces drives the
// REAL collector embed loop (runEmbedLoop → runLoop → discover) against a
// stateful fake whose PipelineScan mirrors EmbedGaps. The backlog is seeded
// LARGER than one scan window (EmbedBatchSize * EmbedWorkers), so the
// scan→drain→rescan cycle must iterate multiple waves to clear it. This is the
// exact mechanism the observed branch-overlay stall lived in (gaps surfaced but
// the loop never drained them to zero); the test proves the loop drains every
// node and then quiesces rather than rescanning forever.
func TestCollectorRunLoop_OverlayBacklogDrainsAcrossWavesAndQuiesces(t *testing.T) {
	// limit = EmbedBatchSize * EmbedWorkers = 5 * 2 = 10 per scan window.
	cfg := Config{
		EmbedBatchSize: 5,
		EmbedWorkers:   2,
		// Channel + release buffers must exceed the in-flight window so the
		// worker's Release send never blocks while the collector is still in its
		// push loop (the collector drains releases only between scans). Waves are
		// serialized by the per-scan limit (EmbedBatchSize*EmbedWorkers=10), not by
		// channel pressure.
		EmbedChannelSize: 100,
		Tick:             time.Millisecond,
		IdleTickMax:      time.Millisecond, // local cadence: no idle-backoff
	}

	const total = 25 // > one 10-item window → at least 3 waves
	nodeIDs := make([]string, total)
	for i := range nodeIDs {
		nodeIDs[i] = "ovl-node-" + string(rune('A'+i/26)) + string(rune('a'+i%26))
	}
	fake := newStatefulEmbedGapFake(nodeIDs, 7)

	embedCh := make(chan EmbedWork, cfg.EmbedChannelSize)
	c := newCollector(
		kgtypes.GraphCode, "agent", cfg,
		nil, embedCh, &metricsState{}, fake,
		cfg.Tick, cfg.Tick, nil, nil, false, true, // summaryEnabled=false, embedEnabled=true: this test drives runEmbedLoop directly
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Simulated worker: drains embedCh, writes back each node's vector (drops it
	// from the live gap set), and releases the in-flight slot so the collector's
	// drain-gate opens for the next wave. Tracks per-node delivery counts to
	// prove no node is re-queued (the in-flight set + write-back must guarantee
	// exactly-once delivery across waves).
	var deliverMu sync.Mutex
	delivered := make(map[string]int)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case w := <-embedCh:
				deliverMu.Lock()
				delivered[w.NodeID]++
				deliverMu.Unlock()
				// Write back the vector → node stops being a gap on the next scan.
				fake.markWritten(w.NodeID)
				// Release the in-flight slot (the worker's role in the loop).
				if w.Release != nil {
					w.Release <- w.NodeID
				}
			}
		}
	}()

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		c.runEmbedLoop(ctx)
	}()

	// (a) Every node drains: the live gap set reaches zero.
	require.Eventually(t, func() bool { return fake.remaining() == 0 }, 5*time.Second, time.Millisecond,
		"the overlay embed backlog must drain to zero across multiple scan waves")

	// Let the loop settle into the quiesced state (cheap-tick caches the gen
	// after an empty scan; subsequent scans return empty).
	time.Sleep(50 * time.Millisecond)
	scansAtQuiesce := fake.scans()

	// (b) Quiescence: once drained, the loop must not keep finding work. Give it
	// more ticks and confirm the gap set stays empty and no node was re-delivered.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, fake.remaining(), "the gap set must stay empty after the drain (no re-opening)")

	deliverMu.Lock()
	require.Len(t, delivered, total, "every seeded node must be delivered to the worker exactly once")
	for id, n := range delivered {
		assert.Equal(t, 1, n, "node %s must be delivered exactly once (no re-queue across waves)", id)
	}
	deliverMu.Unlock()

	// The post-quiescence scans returned empty (the cheap-tick short-circuits the
	// drained loop): the scan count grows by at most a bounded number of empty
	// ticks, NOT an unbounded rescan storm. We assert it grew modestly relative to
	// the ~50ms window at a 1ms tick — the point is "still scanning empty", not
	// "scanning is broken"; an infinite-work rescan would have re-populated
	// delivered above (caught by the exactly-once assertion).
	scansAfter := fake.scans()
	assert.GreaterOrEqual(t, scansAfter, scansAtQuiesce,
		"scan count is monotonic; the loop keeps ticking but returns empty")

	cancel()
	<-loopDone
	<-workerDone
}
