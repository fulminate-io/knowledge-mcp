// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"fmt"
	"log/slog"
)

// similarity_async.go holds the ASYNC wrapper around the similarity lever worker
// (RunSimilarityPass, similarity_lever.go). The reflection single-flight guard is
// claimed HERE, in the trigger path, BEFORE the goroutine launches — so a second
// trigger arriving while a pass runs coalesces (started=false) without spawning a
// duplicate recompute, and the pass body itself never re-acquires (no self-deadlock).
// The pass then runs on a daemon-lifetime goroutine that outlives the request,
// deriving its ctx from p.baseCtx (bind-first startup) so Stop()'s baseCancel aborts an
// in-flight pass; Stop() also drains it via the inFlight WaitGroup.

// SimilarityComplete is the completion callback the tools side supplies to render
// and persist the finished pass. It is invoked exactly once per STARTED pass, from
// inside the async goroutine after RunSimilarityPass returns (or panics — see
// StartSimilarityPass). It is the no-import-cycle seam: tools imports thought, so
// the renderer/persister cannot live here; the tools-side closure does that work.
type SimilarityComplete func(report SimilarityReport, err error)

// StartSimilarityPass is the async entry point behind the manual similarity lever.
// It acquires the reflection single-flight guard SYNCHRONOUSLY in the caller's
// goroutine; if the guard is already held it returns started=false WITHOUT spawning
// and WITHOUT invoking either callback (the caller renders the "already running"
// contract). On an uncontended acquire it invokes onStarted (if non-nil)
// SYNCHRONOUSLY before launching the goroutine and before returning, then spawns a
// daemon-lifetime goroutine that runs RunSimilarityPass and invokes onComplete with
// the result, releasing the guard on every exit path.
//
// ID-FLOW CONTRACT (load-bearing): onStarted runs synchronously on the
// uncontended-acquire path ONLY, before the `go` statement and before this returns.
// It is the SOLE site the tools-side BeginSimilarityEvent fires — the handler's
// onStarted closure creates the status=running event and stashes its id in a
// closure-shared local that onComplete reads later in the goroutine. Because
// onStarted completes-before both goroutine creation and the function return, that
// write happens-before the goroutine's read AND before the handler builds its
// response — no race, no extra synchronization. The handler must NOT call
// BeginSimilarityEvent itself before StartSimilarityPass: gating the event-create
// behind onStarted (which fires only on a real acquire) is exactly what makes a
// coalesced trigger create no orphan running event.
//
// Stop-drain: the goroutine outlives this call, so it adds its OWN inFlight bracket
// (Add before `go`, Done deferred inside) covering RunSimilarityPass AND the
// onComplete persistence. The inner RunSimilarityPass inFlight bracket only covers
// the synchronous worker call; without the wrapper bracket Stop() could return
// while onComplete is still persisting.
//
// Daemon-lifetime ctx: the goroutine runs on a ctx derived from p.baseCtx (the
// loop runs its passes on p.baseCtx too — the bind-first startup change), so Stop()'s baseCancel aborts
// an in-flight pass; Stop() ALSO drains via inFlight.Wait() for the cooperative
// completion path. The request ctx is NEVER used — it dies when the handler returns.
//
// Nil-safe receiver (mirrors RunSimilarityPass / ForceFullPass / Stop): a nil loop
// or nil gc returns started=false without touching the guard.
func (p *PropagationLoop) StartSimilarityPass(
	linkThreshold, mergeThreshold float64,
	densify DensifyParams,
	onStarted func(),
	onComplete SimilarityComplete,
) (started bool) {
	if p == nil || p.gc == nil {
		return false
	}

	release, ok := AcquireReflectionPass(ReflectionPassKey)
	if !ok {
		// Coalesce: another pass already holds the guard. No spawn, no callbacks —
		// the caller renders the "already running" contract; creating no event here
		// is what keeps a coalesced trigger from leaving an orphan running record.
		return false
	}

	// The sole site onStarted (→ BeginSimilarityEvent) fires: synchronously, on the
	// real acquire, before the goroutine and before we return.
	if onStarted != nil {
		onStarted()
	}

	// Bracket Stop-drain for the WHOLE async pass (worker + onComplete persistence),
	// not just the inner RunSimilarityPass bracket. inFlight.Go does the Add/Done so
	// Stop() waits for this goroutine — covering RunSimilarityPass AND onComplete.
	p.inFlight.Go(func() {
		defer release()

		// A panic in the pass must not take down the daemon and must still release
		// the guard (the defer above) AND drive onComplete — degrade, don't die.
		rep := SimilarityReport{}
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("similarity pass panicked: %v", r)
					slog.Error("thought: similarity lever — pass panicked, recovered", "panic", r)
				}
			}()
			// Derive from p.baseCtx (bind-first startup) so a daemon Stop aborts an in-flight
			// async similarity pass rather than pinning the process for its budget.
			rep, err = p.RunSimilarityPass(p.baseContext(), linkThreshold, mergeThreshold, densify)
		}()

		if onComplete != nil {
			onComplete(rep, err)
		}
	})
	return true
}
