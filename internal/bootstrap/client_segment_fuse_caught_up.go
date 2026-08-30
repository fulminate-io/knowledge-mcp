// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// client_segment_fuse_caught_up.go holds ONE predicate. It lives in its own file
// beside client_segment_heal_need.go so that file stays inside its context cap, and
// because the predicate is deliberately standalone: it is a NECESSARY condition on
// the exact per-arm balance verdict, and it is never sufficient on its own.

// fuseCaughtUp reports whether this client has fused through at least the newest
// change the server recorded for (gt, name), with a reason string for every
// decline.
//
// WHAT IT PROVES, AND WHAT IT DOES NOT. Fuse quiescence proves nothing UNFUSED is
// owed — it does not prove the local corpus is complete. A caught-up fuse over a
// corpus that lost documents to a bad merge is still caught up. So this is one
// conjunct of the balance verdict, never the verdict.
//
// THE COMPARISON IS >=, NOT EQUALITY, and that is a correctness point rather than
// leniency. The local watermark advances to the SERVED SAFE HORIZON — roughly now
// minus a couple of seconds — so it routinely sits PAST the newest change stamp. An
// equality test would essentially never hold, and the verdict composing this would
// then never fire.
//
// TWO STAMPERS, NAMED EXPLICITLY BECAUSE THIS IS A COMPARISON ACROSS AUTHORITIES.
// The left side is stamped by THIS CLIENT's merge commit (commitMergeWatermark,
// advanced only after ReEmitDirtyBuckets succeeded). The right side is stamped by
// the SERVER on vector writes and erasure appends. They are different clocks on
// different machines. That is tolerable ONLY because the question is one-sided —
// "have I fused at least this far" — and never an equality; a caller that turned it
// into an equality or a difference would be reading a skew between two clocks as a
// measurement.
//
// EVERY DECLINE CARRIES ITS OWN REASON rather than a bare false, because the four
// declines mean different things to an operator: not-yet-polled is transient,
// unseeded is a graph that has fused nothing, behind is real lag, and an unreadable
// watermark is a fault. Collapsing them would make the composed verdict's "why not"
// unanswerable.
//
// IT NEVER TREATS A MISSING OPERAND AS CAUGHT UP. An absent stamp or an unreadable
// watermark declines. Defaulting either way to "caught up" would let the verdict
// fire on an unfused corpus and rebuild a graph whose documents are merely in
// flight — the false-unhealthy this whole line of work exists to eliminate.
//
// PERF: one durable read the reconcile path already performs, plus one field off a
// gen-poll response the client already receives. No new RPC.
func (c *client) fuseCaughtUp(ctx context.Context, gt kgtypes.GraphType, name string) (bool, string, error) {
	_ = ctx // the two reads are local; ctx is kept for signature stability with its callers

	if c.segmentMgr == nil {
		return false, "no segment manager wired on this client", nil
	}
	if c.serverSegmentStamp == nil {
		// NO READER MEANS NO OBSERVATION, and it declines rather than defaulting. The
		// reader is a func field rather than a direct c.pipeline call so the operand
		// can be supplied by whatever holds it — the same injection idiom
		// localPresence and the collect-gate factory already use — and so this
		// predicate stays answerable without standing up a poll loop.
		return false, "no server change-stamp reader wired on this client", nil
	}

	serverStamp, sampled := c.serverSegmentStamp(gt, name)
	if !sampled {
		// NOT the same as a zero stamp: the bulk poll has not covered this graph yet,
		// so nothing is known about the server side and no comparison is possible.
		return false, "the gen poll has not yet sampled this graph, so the server change stamp is unknown", nil
	}

	localWatermark, err := c.segmentMgr.LoadMergeWatermark(gt, name)
	if err != nil {
		// A FAULT, NOT A VERDICT. Returned as an error so a caller cannot mistake it
		// for a measured "behind", and so it reaches whatever logging the caller has
		// rather than being absorbed into a boolean.
		return false, "the local merge watermark could not be read", fmt.Errorf(
			"bootstrap.fuseCaughtUp %s/%s: load merge watermark: %w", gt, name, err)
	}

	if localWatermark == 0 {
		// AN UNSEEDED GRAPH HAS FUSED NOTHING, and must not be judged. Correct-
		// conservative: it is not caught up, whatever the server stamp says — including
		// when the server stamp is also zero, because "neither side has anything" is
		// not evidence that this client is current with a corpus it has never read.
		return false, "the local merge watermark is zero: this graph has fused nothing yet", nil
	}

	if localWatermark < serverStamp {
		return false, fmt.Sprintf(
			"the local merge watermark %d is behind the server change stamp %d",
			localWatermark, serverStamp), nil
	}
	return true, "", nil
}
