// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// circuitBreaker is a latched pause gate guarding ONE axis's workers. The
// pipeline owns two independent instances — one for the summary axis, one for
// the embed axis. It is the sibling of errBackoff: where the backoff gate
// throttles a transient rate limit with a self-clearing time window, the breaker
// LATCHES its axis's workers to a paused state on a zero-success storm and stays
// there until a human resumes — there is NO self-heal and NO auto-probe.
//
// Trip semantics. The breaker has TWO independent trip conditions; whichever
// fires first latches this axis.
//
// (1) The ZERO-SUCCESS WINDOW. The invariant: trip iff a window of observed
// LLM-call results on THIS axis has had >= 1 attempt and ZERO successes. A
// "window" is the run of call results since the last success. It is realized
// with a single per-instance consecutive-error counter:
//
//   - recordOK()    zeroes consecErrors and empties classCounts — a success on
//     THIS axis proves this axis is not fully dead, so it resets the failure
//     accumulation.
//   - recordErr(class) increments consecErrors, tallies the error's class in
//     classCounts, and trips only once consecErrors reaches tripThreshold with
//     NOT ONE intervening success.
//
// Because every success zeroes the counter, an isolated failure amid
// concurrent successes is MATHEMATICALLY UNABLE to trip: the intervening
// successes keep resetting consecErrors below threshold. The breaker fires
// only when tripThreshold consecutive errored calls land with zero successes
// on THIS axis — a genuine quota / auth / parse / repeated-timeout storm
// where everything in flight on this axis is failing right now.
//
// (2) The DETERMINISTIC SAME-CLASS FAST-TRIP. A SECOND, earlier trip path for
// the case where the window's full count would only burn billed API calls:
// consecDeterministic counts consecutive failures of the SAME
// deterministic-terminal class (IsDeterministicTerminal — parse / invalid-request
// / truncation), and recordErr trips at the much smaller
// DefaultDeterministicFastTripThreshold (2). A same-class deterministic repeat
// reproduces identically for the same batch + config, so the second failure
// proves the rest of the window would fail too — there is no value in burning the
// full zero-success window of discarded API rounds. Any success, any
// non-deterministic errored class, or a DIFFERENT deterministic class resets this
// streak. This path is strictly ADDITIVE — consecErrors still advances on every
// errored call, so the window in (1) is unchanged.
//
// Naming the storm. classCounts is the per-class breakdown of the current
// zero-success window: each errored call is classified (ErrClass) and tallied,
// so when the window trips, autoTripReasonLocked names the DOMINANT class with
// counts instead of a fixed generic string. The window's class tally is reset
// by recordOK() and resume() exactly alongside consecErrors.
//
// Per-axis independence. Each axis (summary, embed) owns its OWN circuitBreaker
// instance with its own consecErrors counter, classCounts tally, pause state,
// and resume channel. A zero-success storm on one axis latches ONLY that axis,
// leaving the healthy axis flowing — so a failing summarizer no longer stalls
// healthy embeddings. The summary worker waits on and records into the summary
// breaker; the embed worker on the embed breaker. The single deliberate
// cross-axis exception is the shared-cause escalation (escalation.go): when one
// axis auto-trips on a DOMINANT auth/quota class AND both axes share the same
// provider, the trip propagates to the other axis — because the protected
// resource (that provider's quota / subscription / auth) is then genuinely
// shared. Provider-distinct axes (e.g. anthropic summaries + voyage embeddings)
// never cross-trip.
type circuitBreaker struct {
	tripThreshold int

	mu           sync.Mutex
	paused       bool
	reason       string
	pausedAt     time.Time
	consecErrors int
	// consecDeterministic and lastDeterministicClass track a SEPARATE, NARROWER
	// window than consecErrors: the streak of consecutive SAME-class
	// deterministic-terminal failures (a class IsDeterministicTerminal reports
	// true for). consecErrors is the class-agnostic zero-success window (threshold
	// tripThreshold, default 20); this streak fast-trips at the much smaller
	// DefaultDeterministicFastTripThreshold (2) because a deterministic-terminal
	// failure reproduces identically — a same-class repeat proves retrying is
	// futile. Both are guarded by the same c.mu as consecErrors. recordErr advances
	// the streak (same class) or resets it (success, non-deterministic class, or a
	// DIFFERENT deterministic class); recordOK and resume zero it.
	//
	// Per-axis note: this streak is per-circuitBreaker-instance state, exactly like
	// consecErrors. With the per-axis breaker split the pipeline owns one breaker —
	// and thus one independent deterministic streak — per axis (summary, embed).
	consecDeterministic    int
	lastDeterministicClass ErrClass
	// classCounts is the per-class breakdown of the current zero-success
	// window: recordErr tallies each errored call's ErrClass here, recordOK and
	// resume clear it. It drives the dominant-class auto-trip reason. Allocated
	// lazily on first recordErr (nil-safe: a nil map reads as empty).
	classCounts map[ErrClass]int
	// resumed is closed (and recreated) on every pause->resume transition to
	// wake all goroutines parked in waitResumed. It is nil when not paused.
	resumed chan struct{}
}

// circuitStatus is the snapshot returned by ONE axis's status(); it carries
// everything pipeline_status and the staleness footer need to render that axis's
// paused line.
//
// DominantClass + DominantCount are the STRUCTURED dominant-class pair (the
// in-package escalation seam the cross-axis coordinator reads). Breakdown is the
// PRE-RENDERED per-class tally line (empty when <= 1 class is present) — it is
// the only class-derived surface that crosses to the tools package, so a
// consumer that reads Reason + Breakdown (both strings) needs zero ErrClass
// knowledge. The field order here mirrors the exported per-axis AxisStatus
// (types.go); circuitStatusToAxis (pipeline.go) does the field-for-field copy
// that crosses one axis's state to the operator-facing PipelineStatus.
type circuitStatus struct {
	Paused        bool
	Reason        string
	Since         time.Time
	DominantClass ErrClass
	DominantCount int
	Breakdown     string
}

// newCircuitBreaker constructs the gate. tripThreshold is the number of
// consecutive errored LLM calls (with zero intervening success on THIS axis)
// that latches THIS axis's workers paused. A non-positive threshold falls back
// to a safe default so a zero-value Config never produces a degenerate gate that
// trips on the first error.
func newCircuitBreaker(tripThreshold int) *circuitBreaker {
	if tripThreshold <= 0 {
		tripThreshold = DefaultCircuitBreakerThreshold
	}
	return &circuitBreaker{tripThreshold: tripThreshold}
}

// recordOK observes one successful LLM call on THIS axis. It zeroes this
// breaker's consecutive-error counter, empties the per-class window tally, and
// ends any deterministic fast-trip streak (the steady-state hot path is a cheap
// no-op when already at zero / empty). A success on this axis proves this axis is
// not fully dead, so it resets both the whole zero-success window and the
// same-class deterministic streak.
func (c *circuitBreaker) recordOK() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecErrors = 0
	clear(c.classCounts)
	c.consecDeterministic = 0
	c.lastDeterministicClass = ClassOther
}

// recordErr observes one errored LLM call of the given class. It advances TWO
// independent trip conditions under the shared lock:
//
//   - The class-agnostic ZERO-SUCCESS WINDOW: consecErrors is incremented on
//     EVERY errored call (deterministic or not) and the class is tallied in
//     classCounts; on reaching tripThreshold (default 20) while not already
//     paused, this axis latches with the synthesized dominant-class reason. Both
//     the transient and terminal worker error branches feed this window, so a
//     repeated-timeout storm trips just as a quota or parse storm does.
//   - The deterministic SAME-CLASS FAST-TRIP streak: when class is
//     deterministic-terminal (IsDeterministicTerminal — ClassParse /
//     ClassInvalidRequest / ClassTruncation), consecutive failures of the SAME
//     such class accumulate in consecDeterministic; on reaching
//     DefaultDeterministicFastTripThreshold (2) while not already paused, this
//     axis latches EARLY with a class-naming fast-trip reason — a same-class
//     deterministic repeat proves a retry is futile, so there is no value in
//     burning the full 20-call window of billed API rounds. Any non-deterministic
//     class, or a DIFFERENT deterministic class, resets the streak (a success
//     resets it via recordOK).
//
// The fast-trip is strictly ADDITIVE: consecErrors still advances by exactly 1
// per errored call for every class, and the zero-success window check is
// unchanged. Because the fast-trip threshold (2) is far below the window
// threshold (20), a same-class deterministic streak trips first; tripLocked is
// idempotent so the deterministic reason wins even if both fire on one call.
//
// It returns tripped == true EXACTLY when THIS call crossed EITHER threshold and
// latched the auto-trip — false when the breaker was already paused (the
// !paused guard short-circuits) or neither counter has reached its threshold.
// That return is the escalation seam the cross-axis coordinator consumes: on
// tripped == true the worker calls Pipeline.escalateOnTrip (escalation.go), which
// reads this axis's status() for the dominant class and cross-trips the other
// axis when the dominant class is auth/quota and both axes share one provider.
func (c *circuitBreaker) recordErr(class ErrClass) (tripped bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecErrors++
	if c.classCounts == nil {
		c.classCounts = make(map[ErrClass]int)
	}
	c.classCounts[class]++

	// Deterministic same-class fast-trip streak (additive to the window below).
	if c.advanceDeterministicStreakLocked(class) {
		c.tripLocked(c.deterministicFastTripReasonLocked(class))
		return true
	}

	if !c.paused && c.consecErrors >= c.tripThreshold {
		c.tripLocked(c.autoTripReasonLocked())
		return true
	}
	return false
}

// advanceDeterministicStreakLocked updates the same-class deterministic streak
// for one errored call and reports whether it has reached the fast-trip
// threshold while the breaker is still running. Caller holds c.mu. A
// deterministic-terminal class extends the streak when it matches the current
// one, or restarts it at 1 on the first failure / a switch to a different
// deterministic class; any non-deterministic class resets the streak (the same
// reset a success performs via recordOK). It does NOT trip — the caller stamps
// the reason — so the streak bookkeeping stays separate from the trip action.
func (c *circuitBreaker) advanceDeterministicStreakLocked(class ErrClass) (fastTrip bool) {
	if !IsDeterministicTerminal(class) {
		// A non-deterministic errored call interleaves and resets the streak.
		c.consecDeterministic = 0
		c.lastDeterministicClass = ClassOther
		return false
	}
	if class == c.lastDeterministicClass {
		c.consecDeterministic++
	} else {
		// First failure of this class, or a switch from a different
		// deterministic class — restart the streak at this class.
		c.consecDeterministic = 1
		c.lastDeterministicClass = class
	}
	return !c.paused && c.consecDeterministic >= DefaultDeterministicFastTripThreshold
}

// classSeverityOrder is the fixed, deterministic ordering used to break ties
// when two classes have an equal count in the window and to render the
// breakdown. Earlier = higher severity / preference. Parse and truncation
// (deterministic-terminal classes an operator most needs named) sort ahead of
// the transient-leaning classes; ClassOther sorts last.
var classSeverityOrder = []ErrClass{
	ClassParse,
	ClassTruncation,
	ClassAuthQuota,
	ClassInvalidRequest,
	ClassTimeoutTransport,
	ClassOther,
}

// dominantClassLocked returns the class with the highest count in the current
// zero-success window and that count. Ties are broken by classSeverityOrder so
// the result is deterministic. Returns (ClassOther, 0) when the window tally is
// empty. Caller holds c.mu. This is the single point where the dominant class
// is computed: both the rendered auto-trip reason and the structured status
// field read from it.
func (c *circuitBreaker) dominantClassLocked() (ErrClass, int) {
	dominant := ClassOther
	best := 0
	for _, class := range classSeverityOrder {
		if n := c.classCounts[class]; n > best {
			best = n
			dominant = class
		}
	}
	return dominant, best
}

// autoTripReasonLocked synthesizes the human-readable pause reason naming the
// dominant error class of the just-closed zero-success window. Caller holds
// c.mu. Shape: "full error round — <count>/<total> <dominant label>", with a
// compact "(breakdown: ...)" suffix in fixed severity order when more than one
// class is present. total == sum(classCounts) == consecErrors.
func (c *circuitBreaker) autoTripReasonLocked() string {
	dominant, count := c.dominantClassLocked()
	total := 0
	distinct := 0
	for _, class := range classSeverityOrder {
		if n := c.classCounts[class]; n > 0 {
			total += n
			distinct++
		}
	}
	reason := fmt.Sprintf("full error round — %d/%d %s", count, total, dominant.Label())
	if distinct > 1 {
		reason += " (breakdown: " + c.breakdownLocked() + ")"
	}
	return reason
}

// deterministicFastTripReasonLocked synthesizes the pause reason for the
// same-class deterministic fast-trip. Caller holds c.mu. It is the early-trip
// counterpart to autoTripReasonLocked: where that names the dominant class of a
// full zero-success round, this names the SAME deterministic class that streaked
// to DefaultDeterministicFastTripThreshold and the saved-call rationale. It reads
// distinctly from the full-round reason and is built generically over the class
// (via ErrClass.Label()) so parse / invalid-request / truncation each render
// correctly.
func (c *circuitBreaker) deterministicFastTripReasonLocked(class ErrClass) string {
	return fmt.Sprintf(
		"fast-trip — %d consecutive %s (deterministic: identical input reproduces the same terminal failure, so retrying only burns billed API calls)",
		c.consecDeterministic, class.Label())
}

// breakdownLocked renders the per-class tally of the current window as a compact
// "class=count" list in fixed severity order (e.g. "parse=18, timeout/transport=2").
// Returns "" when <= 1 distinct class is present (nothing to break down). Caller
// holds c.mu. This is the pre-rendered string surfaced to the tools package via
// circuitStatus.Breakdown so tools needs zero ErrClass visibility.
func (c *circuitBreaker) breakdownLocked() string {
	parts := make([]string, 0, len(classSeverityOrder))
	for _, class := range classSeverityOrder {
		if n := c.classCounts[class]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", class.shortLabel(), n))
		}
	}
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts, ", ")
}

// waitResumed blocks while this axis's breaker is paused, returning when the
// axis is resumed or ctx is canceled. It returns immediately (single mutex
// acquire) in the steady state. It NEVER holds the lock while blocked: the
// ctx.Done() path is what lets a worker parked here unblock on Pipeline.Stop /
// ctx-cancel, so the worker WaitGroup never hangs on shutdown.
func (c *circuitBreaker) waitResumed(ctx context.Context) {
	for {
		c.mu.Lock()
		if !c.paused {
			c.mu.Unlock()
			return
		}
		ch := c.resumed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ch:
			// Resumed (or re-paused since); loop to re-check under the lock.
		}
	}
}

// pause latches this axis paused with an operator-supplied reason. Idempotent:
// pausing an already-paused breaker refreshes the reason but keeps the
// original pausedAt and does not disturb parked waiters.
func (c *circuitBreaker) pause(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tripLocked(reason)
}

// tripLocked sets the paused state. Caller holds c.mu. Idempotent on an
// already-paused breaker except for refreshing the reason; the broadcast
// channel is created on the first transition into paused so waiters have
// something to select on.
func (c *circuitBreaker) tripLocked(reason string) {
	c.reason = reason
	if c.paused {
		return
	}
	c.paused = true
	c.pausedAt = time.Now()
	if c.resumed == nil {
		c.resumed = make(chan struct{})
	}
}

// resume clears the paused state and wakes every parked waiter by closing the
// broadcast channel. It also zeroes this axis's error counter AND the
// deterministic fast-trip streak so the resumed axis starts from a clean window:
// after resume a full FRESH same-class streak (not one carried-over failure) is
// required to re-fast-trip, keeping recovery via resume_pipeline +
// clear_llm_failures unaffected. resume is the ONLY exit from a circuit break —
// there is no self-heal. Idempotent: resuming a running breaker is a no-op.
func (c *circuitBreaker) resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecErrors = 0
	clear(c.classCounts)
	c.consecDeterministic = 0
	c.lastDeterministicClass = ClassOther
	if !c.paused {
		return
	}
	c.paused = false
	c.reason = ""
	c.pausedAt = time.Time{}
	if c.resumed != nil {
		close(c.resumed)
		c.resumed = nil
	}
}

// status returns a snapshot of the breaker's paused state for surfacing to
// operators (pipeline_status, staleness footer).
func (c *circuitBreaker) status() circuitStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	dominant, count := c.dominantClassLocked()
	return circuitStatus{
		Paused:        c.paused,
		Reason:        c.reason,
		Since:         c.pausedAt,
		DominantClass: dominant,
		DominantCount: count,
		Breakdown:     c.breakdownLocked(),
	}
}
