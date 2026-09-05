// SPDX-License-Identifier: Apache-2.0

// collector_evict.go — the durable-not-found arm of the collector's scan loop.
//
// THE CONVERGENCE RATIONALE, IN FULL, because this is the file with room for it.
// The scan-error branch in collector.go applies a CAPPED exponential backoff and
// never gives up, which is right for a transient failure and wrong for a
// permanent one. A graph the server says does not exist produced a scan failure
// per axis per minute, forever, with no producer that could ever satisfy it. A
// lane that can fire forever on one cause is hiding a defect rather than
// repairing a condition, so this arm ENDS the lane instead of backing it off.
//
// WHY THE ORDERING IN THE SCAN-ERROR BRANCH IS THE BEHAVIOUR. The call is the
// branch's FIRST statement, ahead of the backoff. The backoff must not run for a
// graph that will never exist; running it first would merely slow the immortal
// loop rather than end it. (The branch insert in collector.go carries no comment
// of its own: that file sits one line under the repo's hard 500-line cap, and a
// two-line comment there pushes it over. The rationale lives here instead.)
//
// WHY THE DISCRIMINATION IS THIS NARROW. Evicting on ANY error would tear down a
// live graph on one transport blip. The condition is the NOT_FOUND
// classification specifically, and it is taken ONLY on the pipeline scan path,
// where a not-found means the whole requested graph is absent — never on a
// routed Execute, where the same code also means a missing node.
//
// WHY THE EVICTION IS A CALLBACK SEAM. The eviction owner is the client; this
// package should not learn how membership is recorded or logged. Same reason
// AttachWorkingSet takes the set behind an accessor.
//
// EVICTION IS NOT PERMANENT. A later successful interaction with a graph that
// does exist re-admits it on the ordinary Admit path, which is what keeps this a
// repair rather than a second denial mechanism.

package pipeline

import "log/slog"

// endLaneOnDurableNotFound reports whether this scan error is a durable
// per-graph not-found, and if so performs the eviction and tells the caller to
// return.
//
// A NIL CALLBACK STILL ENDS THE LANE. The return value is the convergence
// property; the eviction is what stops the catalog re-registering the collector
// on the next refresh pass. A test fake that wires no callback still gets the
// first, and that is deliberate rather than incidental.
func (c *collector) endLaneOnDurableNotFound(axis string, err error) bool {
	if !isGraphNotFound(err) {
		return false
	}
	slog.Info("pipeline.collector: graph not found; ending lane",
		"graph_type", c.gt, "name", c.name, "axis", axis)
	if c.evictOnDurableNotFound != nil {
		c.evictOnDurableNotFound("durable_not_found")
	}
	return true
}
