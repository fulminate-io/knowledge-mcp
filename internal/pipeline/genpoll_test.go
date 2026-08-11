// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// These are the load-bearing per-tick RPC-count tests for the two-phase bulk
// gen-poll: a no-change tick costs exactly ONE PipelineGenPoll and ZERO
// PipelineScan detail fetches; a tick after M (graph,axis) pairs changed costs ONE
// gen-poll + exactly M detail fetches; WakeAll triggers ONE gen-poll (not a 2N scan
// fan-out); and a per-axis watermark advances only after the backlog drains.
//
// They drive genPollOnce + discover DIRECTLY (no collector goroutines) so the RPC
// counts are deterministic. registerStubCollector wires a real collector into the
// pipeline's registry (so genPollOnce sees its graph + can poke its wakes) WITHOUT
// launching its run loop, which would race the assertions.

const genPollCfgBatch = 5 // SummaryBatchSize/EmbedBatchSize for these tests

// genPollTestPipeline builds a Pipeline with both axes enabled (non-nil
// summarizer + embedder) and a hour-long cadence so nothing fires on its own.
func genPollTestPipeline(t *testing.T, fake *fakeWireClient) *Pipeline {
	t.Helper()
	cfg := Config{
		SummaryBatchSize: genPollCfgBatch, SummaryWorkers: 1,
		EmbedBatchSize: genPollCfgBatch, EmbedWorkers: 1,
		CloudTick: time.Hour, IdleTickMax: time.Hour,
	}
	return New(cfg, fake, (&fakeSummarizer{}).call, (&fakeEmbedder{vectors: map[string][]byte{}}).call)
}

// genPollGT is the graph type for every graph these tests register — all code
// graphs, so the type is fixed.
const genPollGT = kgtypes.GraphCode

// registerStubCollector wires a real collector for (genPollGT, name) into p's
// registry (collectorCancels + collectorWakes) so genPollOnce sees the graph and
// can poke its per-axis wakes — but does NOT launch the run goroutine. The
// collector's genSnapshot reader is bound to p.genSnapshotFor, exactly as
// RegisterGraph wires it, so discover consults the same snapshot genPollOnce writes.
func registerStubCollector(p *Pipeline, name string) *collector {
	c := newCollector(genPollGT, name, p.cfg, p.summaryCh, p.embedCh, p.metrics, p.client,
		p.cfg.TickOrDefault(), p.cfg.TickOrDefault(), nil, nil,
		p.summaryEnabled(), p.embedEnabled(), p.genSnapshotFor, nil)
	key := graphKey{GraphType: genPollGT, GraphName: name}
	p.collectorMu.Lock()
	p.collectorCancels[key] = func() {}
	p.collectorWakes[key] = []chan struct{}{c.summaryWake, c.embedWake}
	p.collectorMu.Unlock()
	return c
}

func entry(name, axis string, gen uint64) *knowledgev1.PipelineGenPollEntry {
	return &knowledgev1.PipelineGenPollEntry{
		GraphType: string(genPollGT), GraphName: name, Axis: axis, DirtyGen: gen,
	}
}

// TestGenPoll_NoChangeTick_OnePollZeroDetail is the core flood-elimination
// guarantee: a tick where every (graph,axis) gen matches the collector's watermark
// issues exactly ONE PipelineGenPoll and ZERO PipelineScan detail fetches. (The
// central loop still POKES the collectors on this first poll — lastPokedGen starts
// at 0 — but the poked discover cheap-ticks on the unchanged gen and issues no
// scan: the poke is a harmless no-op, the detail fetch is what we eliminate.)
func TestGenPoll_NoChangeTick_OnePollZeroDetail(t *testing.T) {
	fake := newFakeWireClient()
	p := genPollTestPipeline(t, fake)
	ctx := context.Background()

	c := registerStubCollector(p, "repoA")
	// The collector has already drained to gen 5 on both axes (its watermark).
	c.lastSummaryGen.Store(5)
	c.lastEmbedGen.Store(5)
	// The poll reports the SAME gen 5 — no change since the last drain.
	fake.seedGenPoll(entry("repoA", "summary", 5), entry("repoA", "embed", 5))

	hint, throttled := p.genPollOnce(ctx)
	require.False(t, throttled)
	require.Zero(t, hint)

	assert.Equal(t, 1, fake.calls["pipeline_gen_poll"], "exactly one bulk gen-poll per tick")

	// Both axes' discover must cheap-tick on the unchanged gen — ZERO detail fetches.
	items, err := c.discover(ctx, "summary", &c.lastSummaryGen)
	require.NoError(t, err)
	assert.Empty(t, items)
	items, err = c.discover(ctx, "embed", &c.lastEmbedGen)
	require.NoError(t, err)
	assert.Empty(t, items)

	assert.Zero(t, fake.calls["pipeline_scan"], "a no-change tick must issue ZERO PipelineScan detail fetches")
}

// TestGenPoll_MChanged_OnePollMDetail seeds a poll that advances exactly M of the
// (graph,axis) pairs and asserts ONE gen-poll + exactly M PipelineScan detail
// fetches: the advanced pairs scan, the unchanged pairs cheap-tick.
func TestGenPoll_MChanged_OnePollMDetail(t *testing.T) {
	fake := newFakeWireClient()
	// scanGaps returns empty items (a drained detail fetch) for both axes so the
	// detail fetch counts without queueing work.
	fake.seedSummaryScan()
	fake.seedEmbedScan()
	p := genPollTestPipeline(t, fake)
	ctx := context.Background()

	cA := registerStubCollector(p, "repoA")
	cB := registerStubCollector(p, "repoB")

	// Advance exactly M=2 pairs: repoA summary and repoB embed. The other two pairs
	// (repoA embed, repoB summary) report gen 0 — matching the cold watermark — so
	// they cheap-tick.
	fake.seedGenPoll(
		entry("repoA", "summary", 2),
		entry("repoA", "embed", 0),
		entry("repoB", "summary", 0),
		entry("repoB", "embed", 2),
	)

	_, throttled := p.genPollOnce(ctx)
	require.False(t, throttled)
	assert.Equal(t, 1, fake.calls["pipeline_gen_poll"], "one bulk gen-poll")

	// Run discover for every (graph,axis): the two advanced pairs scan, the two
	// unchanged pairs cheap-tick.
	for _, dc := range []struct {
		c    *collector
		axis string
		last *atomic.Uint64
	}{
		{cA, "summary", &cA.lastSummaryGen},
		{cA, "embed", &cA.lastEmbedGen},
		{cB, "summary", &cB.lastSummaryGen},
		{cB, "embed", &cB.lastEmbedGen},
	} {
		_, err := dc.c.discover(ctx, dc.axis, dc.last)
		require.NoError(t, err)
	}

	assert.Equal(t, 2, fake.calls["pipeline_scan"], "exactly M=2 detail fetches for the 2 advanced pairs")
	assert.Equal(t, 1, fake.scansByAxis["summary"], "repoA summary advanced → one summary detail scan")
	assert.Equal(t, 1, fake.scansByAxis["embed"], "repoB embed advanced → one embed detail scan")
}

// TestGenPoll_WakeAll_TriggersOnePollNotFanout proves WakeAll triggers exactly ONE
// central gen-poll (drained from genPollWake) rather than a 2N per-collector scan
// fan-out: after WakeAll, draining the wake + running one genPollOnce increments the
// gen-poll count by exactly 1, and with no advanced gens issues ZERO scans.
func TestGenPoll_WakeAll_TriggersOnePollNotFanout(t *testing.T) {
	fake := newFakeWireClient()
	p := genPollTestPipeline(t, fake)
	ctx := context.Background()

	registerStubCollector(p, "repoA")
	registerStubCollector(p, "repoB")
	// The poll reports nothing advanced (no entries → no pokes, no scans).
	fake.seedGenPoll()

	p.WakeAll()
	// WakeAll must have queued exactly one coalesced signal on genPollWake.
	select {
	case <-p.genPollWake:
	default:
		t.Fatal("WakeAll must signal genPollWake")
	}
	// A second drain attempt finds nothing (coalesced single signal).
	select {
	case <-p.genPollWake:
		t.Fatal("WakeAll must coalesce to a single queued signal")
	default:
	}

	// The signal the loop drained drives exactly one bulk poll.
	_, throttled := p.genPollOnce(ctx)
	require.False(t, throttled)
	assert.Equal(t, 1, fake.calls["pipeline_gen_poll"], "WakeAll → exactly ONE gen-poll")
	assert.Zero(t, fake.calls["pipeline_scan"], "WakeAll with no advanced gens → ZERO scans (not a 2N fan-out)")
}

// TestGenPoll_CatalogGenChangeWakesCatalog pins the account CATALOG watermark
// compare inside genPollOnce. Per the proto contract the served value is a
// per-replica SAMPLE, so ANY change — including a backward move — counts as
// movement, while an unchanged value and the 0 a server serves before its first
// bump must never wake the catalog loop. The first observation only records: the
// boot pass already enumerated the catalog.
func TestGenPoll_CatalogGenChangeWakesCatalog(t *testing.T) {
	fake := newFakeWireClient()
	p := genPollTestPipeline(t, fake)
	ctx := context.Background()
	registerStubCollector(p, "repoA")

	// First observation at 7: recorded, no wake.
	fake.seedGenPollCatalog(7, entry("repoA", "summary", 1))
	_, throttled := p.genPollOnce(ctx)
	require.False(t, throttled)
	assert.False(t, drainWake(p.catalogWake), "the first catalog observation records without waking")

	// Unchanged: no wake.
	fake.seedGenPollCatalog(7, entry("repoA", "summary", 1))
	_, _ = p.genPollOnce(ctx)
	assert.False(t, drainWake(p.catalogWake), "an unchanged catalog_gen must not wake the catalog loop")

	// Moved forward: wake. This is the known-positive control proving the
	// no-wake assertions above are not vacuous.
	fake.seedGenPollCatalog(8, entry("repoA", "summary", 1))
	_, _ = p.genPollOnce(ctx)
	assert.True(t, drainWake(p.catalogWake), "a moved catalog_gen must wake the catalog loop")

	// Moved BACKWARD (a different replica's sample): still movement, still a wake.
	fake.seedGenPollCatalog(6, entry("repoA", "summary", 1))
	_, _ = p.genPollOnce(ctx)
	assert.True(t, drainWake(p.catalogWake), "a BACKWARD catalog_gen move is movement — compare for change, not increase")

	// 0 is skipped entirely: no wake, and it must not clobber the last-seen 6
	// (proved by the next non-zero 6 being treated as UNCHANGED).
	fake.seedGenPollCatalog(0, entry("repoA", "summary", 1))
	_, _ = p.genPollOnce(ctx)
	assert.False(t, drainWake(p.catalogWake), "catalog_gen 0 is skipped, never treated as movement")
	fake.seedGenPollCatalog(6, entry("repoA", "summary", 1))
	_, _ = p.genPollOnce(ctx)
	assert.False(t, drainWake(p.catalogWake), "a 0 response must not clobber the last-seen catalog_gen")
}

// TestGenPoll_WatermarkAdvanceOnlyAfterDrain proves the per-axis watermark advances
// only after the backlog drains: the first poll advances a pair's gen and discover
// drains it to empty (advancing the watermark via last.Store); a second poll
// reporting the SAME gen then cheap-ticks with ZERO further detail fetches.
func TestGenPoll_WatermarkAdvanceOnlyAfterDrain(t *testing.T) {
	fake := newFakeWireClient()
	// A drained detail fetch (empty items) carrying the SAME gen the poll reports
	// (3) so discover advances the watermark to 3. In production the poll snapshot
	// gen and the PipelineScan gen come from the same GetPipelineGen source and
	// agree; the fixture models that (seedSummaryScan hardcodes gen 1, which would
	// not match the poll, so set the field directly).
	fake.summaryScanResp = &knowledgev1.PipelineScanResponse{DirtyGen: 3}
	p := genPollTestPipeline(t, fake)
	ctx := context.Background()

	c := registerStubCollector(p, "repoA")

	// Tick 1: gen advances to 3.
	fake.seedGenPoll(entry("repoA", "summary", 3), entry("repoA", "embed", 0))
	_, throttled := p.genPollOnce(ctx)
	require.False(t, throttled)
	// discover sees snap(3) != watermark(0) → issues the detail fetch; the empty
	// result advances the watermark to 3.
	items, err := c.discover(ctx, "summary", &c.lastSummaryGen)
	require.NoError(t, err)
	assert.Empty(t, items)
	assert.Equal(t, uint64(3), c.lastSummaryGen.Load(), "watermark advances to the drained gen after an empty fetch")
	scansAfterTick1 := fake.scansByAxis["summary"]
	require.Equal(t, 1, scansAfterTick1, "tick 1 issues exactly one summary detail fetch")

	// Tick 2: the poll reports the SAME gen 3 — no change.
	fake.seedGenPoll(entry("repoA", "summary", 3), entry("repoA", "embed", 0))
	_, throttled = p.genPollOnce(ctx)
	require.False(t, throttled)
	items, err = c.discover(ctx, "summary", &c.lastSummaryGen)
	require.NoError(t, err)
	assert.Empty(t, items)
	assert.Equal(t, scansAfterTick1, fake.scansByAxis["summary"],
		"tick 2 (unchanged gen, post-drain) issues ZERO further detail fetches — cheap-tick holds")
}
