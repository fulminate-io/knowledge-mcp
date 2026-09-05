// SPDX-License-Identifier: Apache-2.0

// client_workingset.go — the *client's ownership of the interaction-earned
// working set: the accessor every background loop gates on, the admission entry
// point the routed-call recorder calls, the collect-side recorder, and the
// removal a drop performs.
//
// A graph enters the set only through a direct user interaction with THAT graph.
// Nothing in this file re-admits a graph on its own behalf, and nothing ages a
// member out. A member leaves on exactly THREE events: the user DROPS that graph,
// the pipeline scan gets a durable per-graph NOT_FOUND (the server reporting that
// the graph does not exist), or the process restarts. The third is not permanent
// — a later successful interaction re-admits through Admit.

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

// RemoveFromWorkingSet forgets (gt, name) and reports whether it was a member.
// It is InWorkingSet's write counterpart and the one thing in this file that
// SHRINKS the set.
//
// THE FILE HEADER'S "nothing ages a member out" STILL HOLDS, and neither caller
// contradicts it. Aging-out is the SET deciding on its own that a graph has gone
// cold; both of these are someone else deciding. A member leaves on an explicit
// drop of that exact graph, on a durable per-graph NOT_FOUND from the pipeline
// scan — which is the SERVER reporting the graph absent, not the set guessing —
// or on a process restart. The not-found eviction is not permanent: a later
// successful interaction re-admits through Admit, which is what keeps it a repair
// rather than a second denial mechanism.
func (c *client) RemoveFromWorkingSet(gt kgtypes.GraphType, name string) bool {
	if c == nil {
		return false
	}
	return c.workingSet.Remove(gt, name)
}

// SegmentStalledSince reports when (gt, name) stopped being able to recover its
// segment coverage, and 0 when it still can. It is the heal breaker's latch stamp.
//
// IT USED TO ASSEMBLE TWO STAMPS AND TAKE THE EARLIER. The second was the segment
// manager's publish coverage-gate suppression, and it is gone with the manifest
// publish, so there is no coverage gate left to become unsatisfiable and nothing to
// re-arm on a resident rise. What survives is the breaker latch, which latches until
// a manual rebuild_segments or a restart.
//
// THE STALLED BAND IS CORRESPONDINGLY NARROWER, and that is a real reduction rather
// than a simplification: a graph whose publishes were being suppressed used to render
// stalled without the breaker having latched, and no such state exists now.
//
// The stamp comes from THIS process's wall clock and is cleared by a restart, so the
// age a caller derives is scoped to this process. The coverage table's caption says
// so rather than letting a reader assume otherwise.
func (c *client) SegmentStalledSince(gt kgtypes.GraphType, name string) int64 {
	if c == nil {
		return 0
	}
	return c.healBreaker.LatchedSince(gt, name)
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
