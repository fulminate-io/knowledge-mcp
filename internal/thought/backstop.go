// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// backstop.go holds the client-side full-pass reflection backstop: the cadence
// decision (decideBackstopForce), the completed-pass watermark advance
// (recordForcedFullPass), the on-demand operator lever (ForceFullPass), and the
// last_full_pass watermark persistence (read at boot, written after a completed
// forced full pass). The hourly PropagationLoop forces a true full Leiden + DeGroot
// recompute once backstopInterval has elapsed since this watermark, resetting the
// Dynamic-Frontier-Leiden incremental approximation drift (runPass shares the body
// between the tick and the manual lever, loop.go).

// ErrReflectionInFlight is returned by ForceFullPass when another reflection pass
// (an hourly tick, a boot detection, or a concurrent manual propagate) already
// holds the per-account single-flight guard. It is a benign coalesce, NOT a
// failure: the in-flight pass already produces fresh reflection state, so the
// operator lever's trigger was absorbed onto it. Callers (the manual propagate
// tool) report it as "absorbed by an in-flight pass", distinct from a real error.
var ErrReflectionInFlight = errors.New("reflection: a pass is already in progress for this account — coalescing onto it")

// decideBackstopForce decides whether THIS tick must force a full-corpus pass and,
// when so, sets forceFullNext under the lock so the next runClusterDetection takes
// the TRUE full Leiden branch (nilling state alone would only rehydrate from
// cluster_id, NOT a full recompute). backstopInterval <= 0 disables the backstop
// (--reflect-backstop-interval=0): never force, preserving the pure
// incremental/quiet-skip path. The manual ForceFullPass lever does NOT call this —
// it sets forceFullNext directly and pins forceFull true regardless of cadence.
func (p *PropagationLoop) decideBackstopForce() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	forceFull := p.backstopInterval > 0 && p.clockNow().Sub(p.lastFullPass) >= p.backstopInterval
	if forceFull {
		p.forceFullNext = true
	}
	return forceFull
}

// ForceFullPass runs a full-corpus reflection pass ON DEMAND — the operator lever
// behind thoughts(propagate, force_full:true). It is the manual twin of the hourly
// backstop tick: it claims the SAME per-account single-flight guard, pins
// forceFullNext so the shared runPass body takes the TRUE full Leiden branch, and
// runs runPass with forceFull=true UNCONDITIONALLY (bypassing the cadence check
// decideBackstopForce applies to the tick). A completed forced pass advances +
// persists lastFullPass exactly as a cadence-forced tick would, resetting the
// backstop clock. On a coalesce (another pass already holds the guard) it returns
// ErrReflectionInFlight WITHOUT running a second concurrent recompute — the
// in-flight pass already produces fresh state. Returns the propagation result so
// the caller can render a summary line. Nil-safe receiver (mirrors Stop).
func (p *PropagationLoop) ForceFullPass(ctx context.Context) (PropagationResult, error) {
	if p == nil || p.gc == nil {
		return PropagationResult{}, errors.New("reflection: propagation loop not running in this process")
	}
	// Bracket for Stop()-drain exactly as the hourly tick does — a manual forced pass
	// is in-flight work Stop must wait for.
	p.inFlight.Add(1)
	defer p.inFlight.Done()

	release, ok := AcquireReflectionPass(ReflectionPassKey)
	if !ok {
		return PropagationResult{}, ErrReflectionInFlight
	}
	defer release()

	// Pin forceFullNext under the lock so the shared runPass body's runClusterDetection
	// skips rehydrate and takes the full Leiden branch. The tick path sets this via
	// decideBackstopForce; the manual lever sets it directly because it bypasses the
	// cadence check entirely.
	p.mu.Lock()
	p.forceFullNext = true
	p.mu.Unlock()

	slog.Info("thought: reflection backstop — manual force_full pass requested", "key", ReflectionPassKey)
	// ctx (the caller's REQUEST ctx) is passed verbatim as runPass's ctxProbe — it
	// scopes the quiet-skip probe + watermark writes and is intentionally left
	// unchanged. NOTE (bind-first startup): runPass builds its 6m COMPUTE ctx from p.baseCtx,
	// not from this ctx, so a daemon Stop (baseCancel) aborts the compute stages of
	// an in-flight manual force_full along with the loop. This is intentional — do
	// NOT change runPass's compute ctx back to context.Background() to "protect" a
	// manual pass; the manual lever coalesces/retries and must not outlive the loop.
	return p.runPass(ctx, true)
}

// recordForcedFullPass advances lastFullPass to now and persists it so the backstop
// cadence restarts from this completed pass. Called ONLY on the completed
// (non-error, non-budget-exceeded) path — a truncated forced pass leaves
// lastFullPass unchanged so the next tick re-forces (cheap with scoped recompute,
// observable via the budget WARN + the forced log). Mirrors the
// writeLastReflectedGen placement.
func (p *PropagationLoop) recordForcedFullPass(ctx context.Context) {
	now := p.clockNow()
	p.mu.Lock()
	sinceLast := now.Sub(p.lastFullPass)
	p.lastFullPass = now
	p.mu.Unlock()
	if err := writeLastFullPass(ctx, p.gc, now); err != nil {
		slog.Warn("thought: failed to persist last-full-pass watermark", "err", err, "at", now)
	}
	slog.Info("thought: reflection backstop — forced full pass",
		"elapsed_since_last", sinceLast.Round(time.Second),
		"interval", p.backstopInterval)
}

// lastFullPassKey is the metadata key under which the RFC3339 timestamp of the
// last completed full-corpus backstop pass is stored. It is a SIBLING key on the
// SAME singleton resource node as the reflect-gen watermark (watermarkNodeID,
// reflect_gen.go) — one node, two independently-written watermark keys. The upsert
// merges this key onto the existing node without clobbering last_reflected_gen.
const lastFullPassKey = "last_full_pass"

// readLastFullPass reads the persisted last-full-pass watermark from the singleton
// resource node. Mirrors readLastReflectedGen (reflect_gen.go): a single O(1) by-id
// query against the shared watermarkNodeID. Returns (zero time, false) when the
// node is absent (first run), the key is unset, or the timestamp fails to parse — a
// false ok means "never ran a full pass", so the first tick anchors by forcing one.
func readLastFullPass(ctx context.Context, gc Caller) (time.Time, bool) {
	if gc == nil {
		return time.Time{}, false
	}
	raw, err := json.Marshal(map[string]any{"id": watermarkNodeID})
	if err != nil {
		return time.Time{}, false
	}
	resp, err := executeViaEngine(ctx, gc, "query", raw)
	if err != nil {
		return time.Time{}, false
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil || len(nodes) == 0 {
		return time.Time{}, false
	}
	v := kgtypes.Value(nodes[0], lastFullPassKey)
	if v == "" {
		return time.Time{}, false
	}
	t, perr := time.Parse(time.RFC3339, v)
	if perr != nil {
		return time.Time{}, false
	}
	return t, true
}

// writeLastFullPass persists t (the clock time of a just-completed full pass) as
// the last-full-pass watermark via an idempotent upsert of the shared singleton
// resource node. Mirrors writeLastReflectedGen (reflect_gen.go): a PLAIN
// executeViaEngine mutate — NOT executeReflectInertMutate. The write is
// reflect-inert BY NODE TYPE (the singleton is a `resource` node and the reflect-gen
// bump is type-gated to thought/charge), so it does not self-advance the reflect
// gen. The upsert sets only last_full_pass, merging onto the existing node so the
// sibling last_reflected_gen key is preserved.
func writeLastFullPass(ctx context.Context, gc Caller, t time.Time) error {
	if gc == nil {
		return nil
	}
	raw, err := json.Marshal(map[string]any{
		"operation": "upsert",
		"id":        watermarkNodeID,
		"type":      "resource",
		"name":      watermarkNodeName,
		"summary":   "Singleton watermark: the reflect dirty-gen and the last full reflection pass time.",
		"metadata":  map[string]string{lastFullPassKey: t.Format(time.RFC3339)},
	})
	if err != nil {
		return err
	}
	if _, err := executeViaEngine(ctx, gc, "mutate", raw); err != nil {
		return err
	}
	return nil
}
