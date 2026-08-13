// SPDX-License-Identifier: Apache-2.0

// loop_workingset_gate.go — the working-set gate on the propagation family's
// BACKGROUND entry points.
//
// The rule is total: a background process must not request or interact with a
// graph in any way unless a direct MCP interaction — a search, a mutate, a
// collect — has touched that graph. Classifying the propagation operations as
// non-admitting stops them ADMITTING a graph; it does nothing to stop them
// RUNNING against one nobody asked about. This file is the second duty.
//
// Every read in this family targets knowledge/default, so one predicate covers
// all of them.

package thought

import (
	"context"
	"log/slog"
)

// WithWorkingSetGate attaches the working-set predicate and its wake channel.
// admitted reports whether knowledge/default has been admitted by a direct user
// interaction; wake is signaled when a graph is admitted, so a freshly admitted
// corpus does not wait out a full tick interval before its first pass.
//
// Both may be nil. A nil predicate reads as NOT admitted and a nil wake blocks
// forever in a select, which is the default-deny direction: an unwired loop does
// no background work rather than silently doing all of it.
func (p *PropagationLoop) WithWorkingSetGate(admitted func() bool, wake <-chan struct{}) *PropagationLoop {
	if p == nil {
		return p
	}
	p.admitted = admitted
	p.wsWake = wake
	return p
}

// knowledgeAdmitted reports whether the background propagation family may touch
// the knowledge graph at all. An absent gate reads as EMPTY, never as
// unrestricted: a missed wiring under-admits (safe and visible) rather than
// restoring account-wide background work.
func (p *PropagationLoop) knowledgeAdmitted() bool {
	if p == nil || p.admitted == nil {
		return false
	}
	return p.admitted()
}

// restoreWatermarkOnce reads the persisted last-full-pass watermark, once per
// process, on the first gated tick.
//
// It runs HERE rather than in Start because Start is the boot path and the read
// is a wire call: performing it at boot would touch the knowledge graph before
// any interaction had admitted it. Deferring it is exactly semantics-preserving
// — lastFullPass is READ in one place, the backstop cadence check, which is on
// the tick path only, so no reader can run before this call. A forced pass that
// completes first sets and persists the watermark itself, so the deferred read
// converges on the correct value rather than clobbering it.
//
// DELETING the read would NOT be equivalent: a zero watermark makes the cadence
// check force a FULL-CORPUS pass on the first tick after every restart, which is
// precisely the cost the watermark exists to avoid.
func (p *PropagationLoop) restoreWatermarkOnce(ctx context.Context) {
	p.mu.Lock()
	if p.watermarkRestored {
		p.mu.Unlock()
		return
	}
	p.watermarkRestored = true
	p.mu.Unlock()

	t, ok := readLastFullPass(ctx, p.gc)
	if !ok {
		return
	}
	p.mu.Lock()
	p.lastFullPass = t
	p.mu.Unlock()
	slog.Debug("thought: restored the last-full-pass watermark on the first admitted tick", "at", t)
}
