// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"fmt"
	"sync"
)

// deletion_refusal.go — how a REFUSED deletion phase reaches the caller's result
// text instead of being reported as plain success.
//
// THE FACT TRAVELS UP, NOT DOWN. A sink learns the refusal from the server's
// Finalize response, several packages below the handler that renders the text, and
// the composition it returns is computed BEFORE the sink runs. So the carrier is a
// recorder the CALLER installs on the context and the sink writes into — the same
// shape the server's own collect path uses for the walk attestation, in the
// opposite direction.
//
// IT IS PER-CALL STATE, WHICH IS THE WHOLE REASON IT IS NOT A FIELD ON THE SINK.
// The client registers ONE sink for the process (DefaultSinkFactory closes over a
// single instance), so a refusal remembered on the sink would surface in the
// result text of the NEXT, unrelated collect. A context-scoped recorder cannot
// outlive the call that made it.
//
// A COLLECT WITH NO RECORDER IS NORMAL, not a wiring bug: every direct sink
// caller, every cascade sub-collect and every test that drives WriteResult itself
// runs without one. Recording is then a no-op and the Error log line the sink
// emits is the whole report.

// deletionRefusalKey is the typed context key for the recorder. A private type
// avoids collisions with any other package's keys.
type deletionRefusalKey struct{}

// DeletionRefusal records that the server refused this collect's deletion phase.
//
// It is mutated by the sink on the collect's own goroutine and read by the handler
// after that collect returns; the mutex is what makes that hand-off safe for a
// sink that reports from a goroutine of its own.
type DeletionRefusal struct {
	mu     sync.Mutex
	reason string
	named  int
}

// WithDeletionRefusal returns a context carrying a fresh recorder, plus the
// recorder to read after the collect finishes.
//
// IT RETURNS BOTH rather than offering a getter, so a caller cannot read a
// recorder it never installed and mistake the zero value for an admitted
// deletion.
func WithDeletionRefusal(ctx context.Context) (context.Context, *DeletionRefusal) {
	r := &DeletionRefusal{}
	return context.WithValue(ctx, deletionRefusalKey{}, r), r
}

// RecordDeletionRefusal records the guard that refused this collect's deletion
// phase, and the number of entries the collect had named. A context carrying no
// recorder is the ordinary case and records nothing.
//
// AN EMPTY reason RECORDS NOTHING, so the caller does not have to branch: the
// admitted case is the same call with the server's empty answer.
func RecordDeletionRefusal(ctx context.Context, reason string, named int) {
	if reason == "" {
		return
	}
	r, ok := ctx.Value(deletionRefusalKey{}).(*DeletionRefusal)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reason, r.named = reason, named
}

// Notice renders the refusal for the collect's result text, and the empty string
// when the deletion phase ran.
//
// IT NAMES THE THREE THINGS AN OPERATOR ACTS ON: that the rows are still in the
// graph, which guard refused, and that a fresh collect is the resolution. A
// notice saying only "refused" would tell the reader to do nothing in particular.
//
// TWO SHAPES, BECAUSE A REFUSAL REACHES TWO DIFFERENT COLLECTS. A DIFF collect
// named the entries the server refused, and their count is the useful fact. A FULL
// collect's basis is DERIVED server-side and the client named NOTHING, so the
// count is zero — and the named sentence then reads "the 0 entries this collect
// named as deleted are STILL in the graph", which tells the operator nothing true.
// The zero-named shape says what actually happened instead.
func (r *DeletionRefusal) Notice() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reason == "" {
		return ""
	}
	if r.named == 0 {
		return fmt.Sprintf(
			"DELETION REFUSED by the server (%s): this collect's deletion phase did not run, so rows this "+
				"collect could not account for are STILL in the graph. "+
				"Nothing was destroyed; re-collect once the reason is resolved.", r.reason)
	}
	return fmt.Sprintf(
		"DELETION REFUSED by the server (%s): the %d entries this collect named as deleted are STILL in the graph. "+
			"Nothing was destroyed; re-collect once the reason is resolved.", r.reason, r.named)
}
