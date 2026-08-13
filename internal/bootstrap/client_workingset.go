// SPDX-License-Identifier: Apache-2.0

// client_workingset.go — the *client's ownership of the interaction-earned
// working set: the accessor every background loop gates on, the admission entry
// point the routed-call recorder calls, and the collect-side recorder.
//
// A graph enters the set only through a direct user interaction with THAT graph.
// Nothing in this file re-admits a graph on its own behalf, and nothing ages a
// member out: a process restart is the only thing that empties the set.

package bootstrap

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// WorkingSet returns the graphs this process has been asked to work with.
// Returns nil for a directly-built test fixture, and every consumer treats nil
// as EMPTY rather than unrestricted, so a missed wiring under-admits.
func (c *client) WorkingSet() *workingset.Set {
	if c == nil {
		return nil
	}
	return c.workingSet
}

// AdmitGraph records a direct user interaction with (gt, name). reason names the
// interaction that earned the admission — the operation term for a routed call,
// "search" for a segment search, "collect" for an ingest — and is logged once,
// on the first admission of that graph, so a daemon's log shows the working set
// growing without a line per search.
func (c *client) AdmitGraph(gt kgtypes.GraphType, name, reason string) {
	if c == nil {
		return
	}
	if c.workingSet.Admit(gt, name, reason) {
		ref, _ := workingset.Normalize(gt, name)
		slog.Info("working set: graph admitted by user interaction",
			"graph_type", ref.GraphType, "graph", ref.Name, "reason", reason)
	}
}

// InWorkingSet reports whether this client maintains (gt, name) — the membership
// predicate the manage(status) coverage table reads to tell a graph nothing is
// servicing from one an arm has given up on. It is one of the two OPTIONAL deps
// capabilities that table type-asserts for; a nil *client (or a fixture that is not a
// *client) simply does not answer.
func (c *client) InWorkingSet(gt kgtypes.GraphType, name string) bool {
	if c == nil {
		return false
	}
	return c.workingSet.Has(gt, name)
}

// SegmentStalledSince reports when (gt, name) stopped being able to recover its
// segment coverage, and 0 when it still can. It is the ASSEMBLY POINT for a fact that
// spans two owners: the heal breaker latch lives on *client and the publish
// coverage-gate suppression lives on the segment manager, so neither can answer alone
// and *client is the one place holding both.
//
// It takes the EARLIER of the two. They are different mechanisms with different reset
// policies — the breaker latches until a manual rebuild_segments or a restart, the
// publish gate re-arms on a resident rise — so either alone is a real stall, and the
// honest answer to "since when" is whichever started first.
//
// Both stamps come from THIS process's wall clock and are cleared by a restart, so
// the age a caller derives is scoped to this process. The coverage table's caption
// says so rather than letting a reader assume otherwise.
func (c *client) SegmentStalledSince(gt kgtypes.GraphType, name string) int64 {
	if c == nil {
		return 0
	}
	latched := c.healBreaker.LatchedSince(gt, name)
	suppressed := int64(0)
	if c.segmentMgr != nil {
		suppressed = c.segmentMgr.CoverageSuppressedSince(gt, name)
	}
	switch {
	case latched == 0:
		return suppressed
	case suppressed == 0:
		return latched
	case suppressed < latched:
		return suppressed
	default:
		return latched
	}
}

// deferInstructionBootstrapUntilAdmitted runs the one-shot instruction bootstrap
// on the first wake at which knowledge/default is admitted, instead of at boot.
// It is a boot-time query plus a create_batch against the knowledge graph, which
// makes it background work against a graph no interaction has earned yet.
//
// Spawned with `go` from the wiring path so the wait is awaited HERE, exiting
// promptly on ctx.Done with no leak — the shape bootDelayReconcile uses.
//
// THE TRADE, stated rather than designed around: on a FRESH INSTALL the
// bootstrap's job is to seed the agent and skill instruction nodes, and the most
// likely first reader of those nodes is a query against the knowledge graph —
// which is itself the admitting interaction. So the very first instruction read
// after a fresh install can observe an unseeded graph, return empty, and only
// then trigger the seeding a second read would find. The window is one call, it
// is self-correcting, and no data is lost. Nothing here pre-seeds or otherwise
// compensates for it.
func (c *client) deferInstructionBootstrapUntilAdmitted(
	ctx context.Context, gc instructionBootstrapGC, rootDir string,
) {
	// Register the waiter BEFORE the first membership check, and CHECK BEFORE
	// WAITING. Both halves matter: this runs on a goroutine the caller spawned, so
	// an admission can land before it is scheduled, and a plain wait-then-check
	// would block for a wake that has already been delivered to nobody. Checking
	// first catches that admission; registering first means any admission after
	// the check is a signal this waiter receives. There is no gap between them.
	wake := c.workingSet.Wake()
	for {
		if c.workingSet.Has(kgtypes.GraphKnowledge, "default") {
			if err := runInstructionBootstrap(ctx, gc, rootDir); err != nil {
				slog.Warn("instruction bootstrap failed; agent/skill nodes will not be seeded this session",
					"error", err)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
			// A wake for some other graph: loop and re-check.
		}
	}
}

// admittingSink records the collect admission and delegates verbatim. It wraps
// the ingest Sink — the terminal destination every write path in the collector
// package routes through — so a collect of ANY graph family admits the graph it
// just produced.
//
// THE SINK IS THE RIGHT SITE because CollectResult carries GraphType and
// GraphName: the COLLECTOR'S OWN authored identity for the graph it produced,
// correct for code, cloud, cicd, web, pdf, logs, practice and every registered
// custom type, with no per-type mapping. Deriving the name at the collect
// intercept instead would be code-only by construction — it yields "" for every
// non-code collector — so a cloud or cicd collect would silently never admit its
// own graph and would then never be enriched.
type admittingSink struct {
	inner collector.Sink
	admit func(gt kgtypes.GraphType, name string)
}

// WriteResult records the admission BEFORE delegating, deliberately. A collect
// whose upload partially fails still means the user asked this client to work on
// that graph, and the reconcile/pipeline work is exactly what would repair the
// partial state. Recording only on success would drop the admission precisely
// when it is most needed.
func (s admittingSink) WriteResult(
	ctx context.Context, collectorName string, result *collectorwire.CollectResult,
) error {
	if s.admit != nil && result != nil {
		s.admit(result.GraphType, result.GraphName)
	}
	return s.inner.WriteResult(ctx, collectorName, result)
}
