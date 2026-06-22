// SPDX-License-Identifier: Apache-2.0

package segmentdist

import "context"

// ensureShippedSeeded lazily seeds shippedIDs from the server's current segment
// set (Source.List(0)) so a fresh process does not re-ship the entire corpus on
// the first ship(). The server is the source of truth; the client re-derives.
// Backed by the idempotent server Put, this seed is an optimization (avoid the
// upload), not a correctness requirement.
//
// RE-ARM ON FAILURE: the seed latches (m.seeded=true) ONLY when List(0) SUCCEEDS.
// A transient List failure returns the error WITHOUT latching, so the next ship
// re-attempts the seed. This replaces the prior sync.Once+seedErr, which consumed
// the Once on the first attempt even when it failed and then returned the cached
// error forever — a single transient List failure permanently disabled shipping
// for the process lifetime. The whole seed runs under shipMu so concurrent ships
// serialize on it (the second waiter sees seeded==true and returns immediately);
// holding the lock across the List RPC is acceptable because seeding is a rare
// once-per-process success and ship() acquires shipMu only after this returns.
//
// CRITICAL: the server keys blobs by graphKey ONLY (no format dimension), so
// List(0) returns BOTH this graph's HNSW and BM25 blobs. shippedIDs must hold
// ONLY THIS engine's format ids — exactly the same keepFormat filter load()
// applies. Seeding a foreign-format id here would make reconcilePrune treat it as
// "shipped but no longer Exported" (this engine never Exports the other format)
// and PRUNE the other format's live segments server-side: e.g. the BM25 ship
// would prune the just-shipped HNSW segments, leaving VectorByID with nothing to
// resolve. The format filter is the fix for that cross-format prune.
func (m *distManager[Q, S]) ensureShippedSeeded(ctx context.Context) error {
	m.shipMu.Lock()
	defer m.shipMu.Unlock()
	if m.seeded {
		return nil
	}
	metas, err := m.source.List(ctx, 0)
	if err != nil {
		// Transient failure — do NOT latch. The next ship re-arms the seed.
		return err
	}
	for _, meta := range metas {
		if !m.keepFormat(meta.Format) {
			continue
		}
		m.shippedIDs[meta.ID] = struct{}{}
	}
	m.seeded = true
	return nil
}
