// SPDX-License-Identifier: Apache-2.0

package ast

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
)

// match_admission.go bounds how many FILES the ast walk may have in flight
// across the whole process at any instant.
//
// WHAT THE MULTIPLIER WAS. runWorkers spawns min(runtime.NumCPU(), len(files))
// workers PER CALL (match_walk.go:90) and nothing above it counts calls, so N
// concurrent ast.Match calls put N x NumCPU files in flight at once. Each of
// those files holds a parse tree whose Go-side node cache the walk materializes
// essentially in full, so the process heap peak scaled with the product rather
// than with either factor.
//
// WHY THE PERMIT IS HELD ACROSS THE WHOLE OF matchFile. That window is exactly
// the tree's reachable lifetime: matchFile parses at match_walk.go:294 and
// defers tree.Close() at :305, but (*BaseTree).Close() calls only
// C.ts_tree_delete — the Tree's unexported cache map[C.TSNode]*Node survives
// until the *Tree itself becomes unreachable, which is when matchFile returns.
// Holding the permit across the os.ReadFile as well as the parse is deliberate:
// the src buffer is up to the 500KB discovery cap and is live for the same
// window.
//
// WHAT THE PERMIT DOES NOT BOUND — TWO TERMS, BOTH NAMED, because a residue list
// that stops at one reads as complete and is not:
//
//   - THE RETAINED RESULTS. The RawMatch values mergeMatches accumulates
//     (count.go:351) outlive matchFile and are not covered. That is a separate
//     term, separately routed, and it is why the peak harness uses a low-yield
//     pattern (peak_concurrency_test.go).
//   - THE PER-WORKER FIXTURES ALLOCATED BEFORE ANY PERMIT IS TAKEN. The worker
//     fan-out is deliberately unchanged, so N concurrent calls still spawn
//     N x min(NumCPU, len(files)) goroutines, and each builds its own
//     treesitter.Parser (match_walk.go:119), its own compiled pattern (:130-140)
//     and its own sub-pattern cache (:141) before reaching the file loop at :145
//     that calls admit. That residue scales with concurrent call count exactly as
//     the tree term used to. It is small enough to sit under this instrument's
//     noise: the post-gate arms put the concurrent peak at or BELOW the
//     sequential one (measured ratios 0.847 and 0.659), which a residue large
//     enough to matter could not produce.
//
// THE SHAPE IS BORROWED, NOT INVENTED. It extends
// cmd/knowledge/internal/pipeline/writeback_admission.go:96:admitWriteback —
// same module, same admit(ctx) func() signature, same discipline that a
// ctx-cancelled acquisition returns a NON-NIL no-op closure so `defer
// admit(ctx)()` is always safe to write and a failed acquisition can never
// release a permit it does not hold. The narrowing from that gate's per-key map
// to a single process-wide gate is deliberate: ast has one resource domain —
// this machine's memory — not one per graph, so admissionGates / gateFor have no
// analog here.
//
// THE PRESCRIPTION OF THE 'unbounded-fanout-from-single-trigger' PATTERN IS
// DEVIATED FROM, WITH REASON. That pattern says to cap concurrent worker STARTS
// at the launcher. Capping at the launcher here would let one long walk hold
// every permit for its whole duration and starve every other caller, because the
// resource is held per FILE rather than per worker. The cap is therefore taken at
// the resource-holding site, and the worker fan-out itself is unchanged.

// walkWorkerCount reports the worker fan-out width for one Match call —
// the min(NumCPU, len(files)) factor this file's header describes, read through
// a var so it has one home.
//
// A PACKAGE VAR RATHER THAN A DIRECT runtime.NumCPU() CALL so the peak
// instrument can PIN it. That instrument's sequential arm holds exactly this
// many parse trees in flight while its concurrent arm saturates against GC
// pacing, so the concurrent/sequential ratio FALLS as the host's core count
// rises — measured 4.825 on a 16-core host against 2.570 on a 22-core one for
// identical code, which put a hardware-dependent floor under a gate whose whole
// question is about the probe rather than about the machine. Production always
// reads NumCPU; only tests replace it.
var walkWorkerCount = runtime.NumCPU

// walkAdmissionGate is the process-wide in-flight file budget. permits is the
// buffered-channel permit store — this module's idiom for a bounded-concurrency
// gate — and inFlight/peak are the census the admission tests read.
type walkAdmissionGate struct {
	permits  chan struct{}
	inFlight atomic.Int64
	peak     atomic.Int64
}

// newWalkAdmission builds a gate admitting slots files at a time.
//
// A non-positive slots count is refused loudly rather than coerced: an
// unbuffered permit channel would block every walk until its context expired,
// which surfaces as an unexplained hang rather than as the configuration error
// it is.
func newWalkAdmission(slots int) *walkAdmissionGate {
	if slots < 1 {
		panic(fmt.Sprintf("ast: walk admission needs at least 1 slot, got %d", slots))
	}
	return &walkAdmissionGate{permits: make(chan struct{}, slots)}
}

// walkAdmission is the single gate every ast walk in this process shares.
//
// WHY runtime.NumCPU() AND NOT A SMALLER NUMBER. A single call already runs
// min(NumCPU, len(files)) workers (match_walk.go:90), so at NumCPU slots an
// UNCONTENDED call never blocks — measured single-call wall 111-137ms uncapped
// against 115-132ms capped, which is noise. Going below NumCPU buys more peak
// for real latency: at one slot the single-call wall goes to 826-833ms and the
// eight-call wall to 3.2s. That is a different trade, and if the budget ever
// proves insufficient that is a routed decision rather than a constant to
// quietly retune.
var walkAdmission = newWalkAdmission(runtime.NumCPU())

// admit blocks until a permit is free or ctx is done, and returns the closure
// that releases it.
//
// On the acquire branch it also raises the high-water census. On the CANCEL
// branch it touches neither counter — an unheld permit must not appear in the
// census — and returns a non-nil no-op, so a cancelled walk proceeds into
// a.tsp.Parse and fails there exactly as it already did, attributed to
// skipParseError by the existing branch at match_walk.go:297. No new skip reason
// is introduced.
func (g *walkAdmissionGate) admit(ctx context.Context) func() {
	select {
	case g.permits <- struct{}{}:
		g.raisePeak(g.inFlight.Add(1))
		return func() {
			g.inFlight.Add(-1)
			<-g.permits
		}
	case <-ctx.Done():
		return func() {}
	}
}

// raisePeak lifts the recorded high-water mark to now when now is higher.
// Compare-and-swap in a loop rather than a plain store: two admissions can
// observe the same old value concurrently, and a losing racer must re-read
// rather than clobber the winner with a stale maximum.
func (g *walkAdmissionGate) raisePeak(now int64) {
	for {
		old := g.peak.Load()
		if now <= old || g.peak.CompareAndSwap(old, now) {
			return
		}
	}
}

// slots reports the gate's budget. It exists so a test can pin the shipped
// budget against being silently widened to a no-op.
func (g *walkAdmissionGate) slots() int { return cap(g.permits) }

// highWater reports the most files this gate ever had admitted at once.
//
// The counter only ever rises, and no reset exists: each test arm that needs a
// fresh census installs a fresh gate, whose counters start at zero.
func (g *walkAdmissionGate) highWater() int64 { return g.peak.Load() }
