// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestReclaimConcurrency drives merges racing with ships and with each other on one
// engine while the reclaim hook fires lock-free post-CAS. Run under -race it proves:
// no data race, no panic, no deadlock; and at quiescence no id ever Removed from L2
// is still live in Export() (the invariant's clause 2, observable only because the
// instrumented seam intercepts Remove). The reclaim callback runs post-CAS holding
// no engine lock, so it must never stall the 50ms merger.
func TestReclaimConcurrency(t *testing.T) {
	ctx := context.Background()
	// Low count target so the background merger fires frequently as segments pile up.
	dm, ic := buildHNSWReclaimManager(t, kgtypes.GraphCode, "concurrency", t.TempDir(), 3)
	defer dm.engine.Close()

	const (
		writers   = 4
		perWriter = 40
	)
	var wg sync.WaitGroup

	// Writers: each seals + ships its own docs concurrently. AddAndShip-style ship
	// against locallyShipped races the background merger and the reclaim hook.
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWriter {
				d := vecContentDocsSeed(1, (w+1)*100000+i)[0]
				if err := dm.engine.Add([]searchengine.Document{d}); err != nil {
					t.Errorf("Add: %v", err)
					return
				}
				// ship warms the L2 cache (Put) and reconcile-prunes concurrently with
				// the background merger's reclaim hook (Put/Remove) — the contended path.
				if _, err := dm.ship(ctx, dm.locallyShipped); err != nil {
					t.Errorf("ship: %v", err)
					return
				}
			}
		}(w)
	}

	// A concurrent searcher to add read pressure across merge swaps.
	wg.Go(func() {
		probe := vecContentDocsSeed(1, 999999)[0]
		for range writers * perWriter {
			_ = dm.engine.Search(probe.Vector, 5)
		}
	})

	wg.Wait()

	// Drain any in-flight merges, then re-warm so the final consolidated set is
	// L2-backed and assert the invariant at quiescence.
	waitMergeQuiesce(dm.engine.MergeCount)
	warmExported(dm)

	// Clause 2 explicitly: no id the reclaim hook removed is still live.
	removed := ic.removedSet()
	liveNow := exportedIDSet(dm)
	for id := range removed {
		_, stillLive := liveNow[id]
		require.Falsef(t, stillLive, "id %s removed from L2 is still live in Export() after concurrency", id)
	}
	assertLiveSetBackedByL2(t, dm, removed, nil, nil)
	require.GreaterOrEqual(t, dm.engine.MergeCount(), uint64(1), "the concurrent run exercised at least one merge")
}
