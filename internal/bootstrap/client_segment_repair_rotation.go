// SPDX-License-Identifier: Apache-2.0

package bootstrap

// client_segment_repair_rotation.go owns the coverage-repair backstop's ROUND-ROBIN
// SCHEDULING: which graph a sweep tick offers a turn to, and which one actually spends
// the tick's single grant.
//
// SPLIT FROM client_segment_repair.go, which keeps the per-graph detector cascade that
// CONSUMES this. The two are separate concerns and separately testable — the cascade
// decides whether one graph needs a repair, and this decides whose turn it is — and
// keeping them in one file had pushed that file past the repo's length cap.
//
// THE OFFER AND THE GRANT ARE TWO EVENTS, and that separation is the whole design here:
// the rotation offers graphs their turn in order, and the tick's one grant is spent only
// once a graph has passed every gate that can decline. See offerRepairSlot for what went
// wrong when the two were one call.

// beginRepairTick resets the per-pass round-robin bookkeeping. Called at the head of
// every reconcile pass.
//
// THE WRAP USES THE PREVIOUS PASS'S OFFER COUNT, which is why segmentRepairSeen is
// kept rather than derived. Wrapping only when a pass granted nothing would cost a
// whole idle tick per rotation, and on a single-graph daemon it would starve that
// graph on every other pass — the cursor would sit at 1 with nothing at index 1 to
// serve.
func (c *client) beginRepairTick() {
	c.segmentRepairMu.Lock()
	defer c.segmentRepairMu.Unlock()
	if c.segmentRepairSeen > 0 && c.segmentRepairCursor >= c.segmentRepairSeen {
		c.segmentRepairCursor = 0
	}
	c.segmentRepairTickGranted = false
	c.segmentRepairSeen = 0
}

// offerRepairSlot decides, under ONE critical section, whether this graph is still a
// CANDIDATE for the pass's repair slot. It does NOT spend the slot — grantRepairSlot
// does, after every gate that can decline.
//
// THE TWO ARE SEPARATE BECAUSE THE TICK'S ONE GRANT MUST BE SPENT ON WORK, NOT ON AN
// OFFER. When the two were one call, taken ahead of the coverage reads, a graph that
// took the slot and then declined at the band, the residue, the disarm or the breaker
// had consumed the tick's only grant — so a graph that genuinely needed a repair waited
// one whole tick per decliner ahead of it in the rotation, at a multi-minute cadence.
// Splitting them means a decliner no longer ends the tick: the sweep keeps offering
// graphs in rotation order until one reaches the grant, so a repair-needing graph is
// serviced in the FIRST tick in which it is offered.
//
// THE OFFER COUNT ADVANCES FOR EVERY GRAPH OFFERED, unchanged, and it must: it is what
// the next pass's wrap compares the cursor against, so it has to measure the WHOLE
// rotation. Stopping it at the grant leaves it reading 1 forever, which wraps the
// cursor every pass and starves every graph but the first.
//
// WHAT IT COSTS A NON-CANDIDATE: one mutex and no reads, exactly what the old claim
// cost. Once the tick HAS granted, every later graph fails here and the rest of the
// tick costs nothing at all.
//
// CONCURRENT PASSES, stated rather than assumed: the flag is per-client and reset per
// pass, and the reconcile pass has no re-entrancy guard, so a boot-delay pass
// overlapping the ticker can admit at most TWO repairs in that window rather than
// one. That is the accepted bound — RepairUncoveredSegments' own single-flight still
// prevents two passes on the SAME graph.
func (c *client) offerRepairSlot() bool {
	c.segmentRepairMu.Lock()
	defer c.segmentRepairMu.Unlock()
	seen := c.segmentRepairSeen
	c.segmentRepairSeen++
	if c.segmentRepairTickGranted {
		return false
	}
	if seen < c.segmentRepairCursor {
		return false // an earlier graph in the rotation owns a later tick's slot.
	}
	return true
}

// grantRepairSlot spends the tick's one grant and advances the rotation, and it is
// called only once a graph has passed every gate that can decline.
//
// THE CURSOR ADVANCES AT GRANT, NOT ON SUCCESS. Rotating only after a SUCCESSFUL repair
// starves every graph behind one whose repair keeps failing — that rule is unchanged;
// what moved is which event counts as taking the turn.
func (c *client) grantRepairSlot() {
	c.segmentRepairMu.Lock()
	defer c.segmentRepairMu.Unlock()
	c.segmentRepairTickGranted = true
	c.segmentRepairCursor++
}
