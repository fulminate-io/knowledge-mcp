// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// collector_loop_helpers.go holds the mechanical work runLoop drives each cycle:
// in-flight set maintenance, the item push, the idle-backoff step, and the two
// sleep primitives. Relocated verbatim from collector.go, which had grown past the
// file-length cap. The loop POLICY — the four throttles and the order they are
// evaluated in — stays in collector.go with runLoop, so a reader looking for "when
// does this collector scan" still finds one file.

// pushNewItems queues every discovered item that is not already in flight onto the
// axis channel, recording each as in-flight as it goes. A duplicate is skipped
// rather than re-queued: that in-flight set is what stopped a slow worker call from
// letting a dozen ticks re-queue the same nodes.
//
// Returns false when the push could not complete (ctx cancelled), which is the
// caller's signal to exit the loop.
func (c *collector) pushNewItems(
	ctx context.Context, ax loopAxis, items []*knowledgev1.PipelineScanItem,
	inFlight map[string]struct{}, release chan string,
) bool {
	for _, item := range items {
		if _, dup := inFlight[item.GetNodeId()]; dup {
			continue
		}
		if !ax.push(ctx, item, release) {
			return false
		}
		inFlight[item.GetNodeId()] = struct{}{}
	}
	return true
}

// nextIdleInterval doubles cur toward (and capped at) max — the idle-backoff
// growth step. Guards against int64 overflow on the doubling.
func nextIdleInterval(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next <= 0 || next > max {
		next = max
	}
	return next
}

// nextScanInterval is throttle #1, the idle-backoff step, decided for one cycle:
// work found → the fast base cadence; an empty scan → grow toward idleMax so a
// fully-drained graph costs about one scan per idleMax. Relocated whole out of
// runLoop, which had grown past the cognitive-complexity cap; the branch, the
// growth step it delegates to and the values it returns are unchanged.
//
// The idle sleep this feeds is the ONLY one a collect wake interrupts — a long
// idle interval should yield immediately to fresh work, but a rate-limit backoff
// (#3) and a drain-gate wait must run to completion. That distinction lives at
// the call site, in which sleep primitive runLoop reaches for; this function
// only decides how long the interval is.
func nextScanInterval(items int, base, cur, idleMax time.Duration) time.Duration {
	if items > 0 {
		return base
	}
	return nextIdleInterval(cur, idleMax)
}

// drainReleases empties the release channel non-blocking, removing each
// released ID from the in-flight set. Called at the start of every cycle
// so the next discovery query sees an accurate in-flight picture.
func drainReleases(release <-chan string, inFlight map[string]struct{}) {
	for {
		select {
		case id := <-release:
			delete(inFlight, id)
		default:
			return
		}
	}
}

// pruneInFlightItems intersects inFlight with the just-discovered eligible
// set: any ID no longer eligible is removed from in-flight. Defense in
// depth against missed releases (e.g., worker panic before release write):
// once Summary is populated or a marker lands, the scan no longer returns
// the ID, so it falls out of in-flight here even if the release was
// dropped.
func pruneInFlightItems(inFlight map[string]struct{}, eligible []*knowledgev1.PipelineScanItem) {
	if len(inFlight) == 0 {
		return
	}
	eligibleSet := make(map[string]struct{}, len(eligible))
	for _, it := range eligible {
		eligibleSet[it.GetNodeId()] = struct{}{}
	}
	for id := range inFlight {
		if _, still := eligibleSet[id]; !still {
			delete(inFlight, id)
		}
	}
}

// sleepFor waits d (falling back to Config.TickOrDefault for a non-positive d)
// or returns false on ctx cancel. The shared loop computes d per cycle from the
// idle-backoff / drain-gate / collect-gate / scan-backoff state.
func (c *collector) sleepFor(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = c.cfg.TickOrDefault()
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// sleepForWake is sleepFor plus an early-return on a wake signal — the idle sleep
// uses it so Pipeline.WakeAll (fired by a collect) cuts a long backed-off
// interval short. A nil wake channel blocks forever on that arm (so the timer /
// ctx still govern), keeping test fakes and wake-less paths unaffected.
//
// Returns (alive, byWake): alive is false ONLY on ctx cancel (the caller exits
// the loop); byWake is true ONLY when the <-wake arm fired (a collect-wake), and
// false on a plain timer expiry. The embed loop reads byWake to ARM the
// auto-heal check — a collect-wake (not an idle timer tick) is what makes the
// heal fire once per collect.
//
// The drain-gate, collect-gate and scan-backoff waits deliberately use sleepFor
// instead: a wake consumed by one of those would be a wake nobody acts on.
func (c *collector) sleepForWake(ctx context.Context, d time.Duration, wake <-chan struct{}) (alive, byWake bool) {
	if d <= 0 {
		d = c.cfg.TickOrDefault()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false, false
	case <-t.C:
		return true, false
	case <-wake:
		return true, true
	}
}
