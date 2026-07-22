// SPDX-License-Identifier: Apache-2.0

package segmentdist

// This file holds the lifecycle-aware publish-gate predicates the embed entry
// points (AddAndShip/AddAndShipFields/Flush) consult before running a ship/publish
// pass. The gate skips a no-progress publish for sub-threshold unsealed batches:
// ship/publish runs iff a SEALED unshipped export exists (hasUnshippedExport) OR a
// prior publish did not land and is pending retry (publishRetryPending). The retry
// bit itself is set/cleared inside publishResident (manager_prune.go), the one site
// that sees every publish outcome.

// hasUnshippedExport reports whether a SEALED unshipped export exists: there is at
// least one exported segment whose id is not yet in shippedIDs (or the seed has not
// yet run). It is a read-only projection of the ship-new diff loop in shipAndPublish
// — same shippedIDs-membership test, returning a bool instead of building the diff.
// Export() is read OUTSIDE shipMu (exactly as shipAndPublish does) because Export
// takes its own engine lock; the shippedIDs membership walk then takes shipMu.
func (m *distManager[Q, S]) hasUnshippedExport() bool {
	exported := m.engine.Export()
	if len(exported) == 0 {
		return false
	}
	m.shipMu.Lock()
	defer m.shipMu.Unlock()
	if !m.seeded {
		return true
	}
	for _, b := range exported {
		if _, sent := m.shippedIDs[b.ID]; !sent {
			return true
		}
	}
	return false
}

// publishRetryPending is the shipMu-guarded read of the publishPending retry bit
// (mirrors the seeded read in ensureShippedSeeded). The embed gate ORs it with
// hasUnshippedExport so a shipped-but-unpublished set re-attempts the publish.
func (m *distManager[Q, S]) publishRetryPending() bool {
	m.shipMu.Lock()
	defer m.shipMu.Unlock()
	return m.publishPending
}

// setPublishPending latches the publishPending retry bit under shipMu. Called from
// publishResident's non-success outcome points (coverage-read error, coverage-gate
// skip, 409 skip, transport error); the success path clears it under the reconcile
// lock it already holds.
func (m *distManager[Q, S]) setPublishPending() {
	m.shipMu.Lock()
	m.publishPending = true
	m.shipMu.Unlock()
}
