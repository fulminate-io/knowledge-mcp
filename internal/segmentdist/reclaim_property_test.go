// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// Property-test budget knobs. The prior single-fixed-seed form re-proved ONE
// 60-op interleaving at ~172s — too narrow to be a real fuzz net and too slow to
// broaden via -count. This form trades depth-per-stream for breadth-across-streams:
// ONE `go test` invocation runs propSeeds DISTINCT seeds (base + index), each a
// fresh random op-stream over propGraphsPerStream real HNSW engines for
// propOpsPerStream ops, asserting the invariant after every op. Tuned so all
// propSeeds streams finish well under Go's 10m default (target ≤ ~90s): small
// per-add corpora + short streams + a tight merge-quiescence window, multiplied
// across many seeds. Deterministic: the same propBaseSeed yields the same
// propSeeds streams every run; set RECLAIM_PROP_SEED=<n> to replay ONE seed.
const (
	propBaseSeed         = int64(0x5E6E70F0)
	propSeeds            = 16 // distinct interleavings explored per invocation
	propOpsPerStream     = 18 // ops per stream (kept short; breadth via propSeeds)
	propGraphsPerStream  = 2  // real HNSW engines interleaved per stream
	propQuiesceStableFor = 40 * time.Millisecond
)

// propGraph is one graph under test in the randomized property test: a real HNSW
// embed engine over the seam-injected instrumented cache, reconstructable across a
// simulated restart (same dir + same caller, fresh distManager).
type propGraph struct {
	name    string
	dir     string
	caller  *fakeSegmentSource
	dm      *distManager[[]byte, struct{}]
	ic      *instrumentedCache
	nextDoc int
}

// TestReclaimPropertyRandomized is the adversarial fuzz net: ONE invocation runs
// propSeeds DISTINCT random op-streams (add / force-merge / ROLE-B ship / ROLE-A
// replace-prune / restart) over real HNSW+BM25-shaped engines, asserting
// assertLiveSetBackedByL2 after EVERY op of EVERY stream. Each stream's seed is
// logged before it runs, so any failure is reproducible. Set RECLAIM_PROP_SEED=<n>
// to replay a single failing seed deterministically.
func TestReclaimPropertyRandomized(t *testing.T) {
	if override, ok := os.LookupEnv("RECLAIM_PROP_SEED"); ok {
		seed, err := strconv.ParseInt(override, 0, 64)
		require.NoErrorf(t, err, "RECLAIM_PROP_SEED=%q must be a base-0 integer (e.g. 0x... or decimal)", override)
		t.Logf("RECLAIM_PROP_SEED override: replaying single seed=%d (0x%x)", seed, uint64(seed))
		runPropertyWithSeed(t, seed)
		return
	}

	// propSeeds distinct streams from deterministically-derived seeds (base + index
	// folded through a multiplier so adjacent seeds are not near-identical streams).
	for i := range propSeeds {
		seed := propBaseSeed + int64(i)*0x9E3779B1 // golden-ratio step for seed spread
		runPropertyWithSeed(t, seed)
	}
}

func runPropertyWithSeed(t *testing.T, seed int64) {
	t.Logf("property stream seed=%d (0x%x) — replay with RECLAIM_PROP_SEED=%d", seed, uint64(seed), seed)
	rng := rand.New(rand.NewSource(seed))
	ctx := context.Background()
	gt := kgtypes.GraphCode

	graphs := make([]*propGraph, propGraphsPerStream)
	for i := range graphs {
		_, gc := newSegmentHarness(t)
		// Seed-scoped graph name so distinct streams never collide on a cache dir or
		// a server graphKey (each stream gets its own t.TempDir() too).
		name := graphNameFor(seed, i)
		dir := t.TempDir()
		dm, ic := buildHNSWReclaimManagerOn(t, gc, gt, name, dir, randCountTarget(rng))
		graphs[i] = &propGraph{name: name, dir: dir, caller: gc, dm: dm, ic: ic}
	}
	defer func() {
		for _, g := range graphs {
			g.dm.engine.Close()
		}
	}()

	checkAll := func() {
		for _, g := range graphs {
			waitMergeQuiesceWindow(g.dm.engine.MergeCount, propQuiesceStableFor)
			warmExported(g.dm)
			assertLiveSetBackedByL2(t, g.dm, g.ic.removedSet(), nil, nil)
		}
	}

	for range propOpsPerStream {
		g := graphs[rng.Intn(len(graphs))]
		switch rng.Intn(5) {
		case 0: // add docs as a MULTI-doc segment (>=3 docs/segment) so a later single
			// delete never empties a segment. The kit's MinSegmentDocs is 1, so a batch
			// Add seals one segment holding the whole batch.
			n := 3 + rng.Intn(3)
			batch := make([]searchengine.Document, n)
			for k := range batch {
				batch[k] = vecContentDocsSeed(1, hashSeed(g.name)+g.nextDoc)[0]
				g.nextDoc++
			}
			require.NoError(t, g.dm.engine.Add(batch))
		case 1: // delete a live doc to make a segment merge-eligible — only when the
			// corpus has ample live docs, so the consolidated segment always retains
			// survivors and is never the empty (v1) blob the engine's Decode rejects on
			// reload (a pre-existing engine constraint, orthogonal to reclaim). We do NOT
			// block on the merge here: the background merger fires it on its ticker and
			// the per-op checkAll's quiescence settles it before the next assertion —
			// blocking on a specific +1 was the dominant per-stream cost.
			if id, ok := anyLiveDocID(g.dm); ok && liveDocCount(g.dm) >= 6 {
				g.dm.engine.Delete(id)
			}
		case 2: // ROLE-B embed ship
			_, err := g.dm.ship(ctx, g.dm.locallyShipped)
			require.NoError(t, err)
		case 3: // ROLE-A: a full-corpus replace-prune ship (the FlushDeterministic shape)
			_, err := g.dm.ship(ctx, g.dm.shippedIDs)
			require.NoError(t, err)
		case 4: // restart: fresh distManager over the same dir + caller, then reload
			g.dm.engine.Close()
			dm, ic := buildHNSWReclaimManagerOn(t, g.caller, gt, g.name, g.dir, randCountTarget(rng))
			g.dm, g.ic = dm, ic
			require.NoError(t, g.dm.load(ctx))
		}
		// Invariant after EVERY operation.
		checkAll()
	}
}

// anyLiveDocID returns one external doc id currently indexed by the engine via a
// zero-vector fan-out probe (any hit's id is a live doc); false when empty.
func anyLiveDocID(dm *distManager[[]byte, struct{}]) (searchengine.ExternalID, bool) {
	probe := make([]byte, 32)
	hits := dm.engine.Search(probe, 1)
	if len(hits) == 0 {
		return "", false
	}
	return hits[0].ID, true
}

// liveDocCount sums the engine's resident doc count — a cheap guard so a delete
// never targets the corpus's last few docs (which could empty a segment and
// produce the v1 blob the engine refuses to decode on reload).
func liveDocCount(dm *distManager[[]byte, struct{}]) int {
	return dm.engine.ResidentDocCount()
}

// graphNameFor names a property graph distinctly per (seed, index) so distinct
// streams never collide on a server graphKey.
func graphNameFor(seed int64, i int) string {
	return "prop-" + strconv.FormatUint(uint64(seed), 36) + "-" + strconv.Itoa(i)
}

// hashSeed derives a stable per-graph doc-id offset so distinct graphs never share
// ids OR vectors.
func hashSeed(name string) int {
	h := 0
	for _, c := range name {
		h = h*131 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return (h % 9000) * 1000
}

// randCountTarget returns a small or large count target so some graphs merge by
// count and others only by dead-ratio — widening the interleaving space.
func randCountTarget(rng *rand.Rand) int {
	if rng.Intn(2) == 0 {
		return 2 + rng.Intn(4) // count-driven merges fire readily
	}
	return 1 << 30 // only dead-ratio merges fire
}
