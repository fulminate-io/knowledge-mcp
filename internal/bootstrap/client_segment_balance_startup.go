// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// client_segment_balance_startup.go holds the BOOT-TIME balance report: one
// evaluation per segment-bearing graph, fired once after startup.
//
// WHY BOOT NEEDS ITS OWN TRIGGER. The quiescence-edge verdict
// (client_segment_balance_quiescence.go) is driven by a COLLECT reaching pipeline
// quiescence, so a daemon that starts against an already-pathological pool and is
// then never collected into forms no verdict at all — the pool is judged for the
// first time at the first rebuild, which may be days away or never. Boot is the
// moment the condition is both present and cheapest to state.

// segmentBalanceBootDelay is the delay before the one-shot boot balance report
// fires. It is deliberately LONGER than segmentReconcileBootDelay so the boot
// reconcile's heal has already run: reporting a shortfall the arm behind it is about
// to repair would name a graph that is not actually sick, and an operator taught to
// discount this line will discount it when it is real.
const segmentBalanceBootDelay = segmentReconcileBootDelay + 30*time.Second

// startupBalance is the per-graph boot verdict, held for the status surface.
//
// IT IS PROCESS-SCOPED AND NEVER REFRESHED, deliberately: it answers "what did this
// pool look like when this daemon started", which is a different question from the
// live coverage the table beside it renders, and a value that quietly updated would
// answer neither.
type startupBalance struct {
	mu       sync.RWMutex
	byGraph  map[segmentGraphRef]string
	reported bool
}

func (s *startupBalance) record(g segmentGraphRef, verdict string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byGraph == nil {
		s.byGraph = map[segmentGraphRef]string{}
	}
	s.byGraph[g] = verdict
	s.reported = true
}

// snapshot returns the recorded verdicts and whether the pass has run at all.
//
// THE SECOND RETURN IS NOT DERIVABLE FROM THE FIRST. A pass that ran and found no
// segment-bearing graphs leaves an empty map, which is the same map a pass that never
// ran leaves — and rendering "no startup verdicts" for the second would report a
// clean boot for a check that never happened.
func (s *startupBalance) snapshot() (map[segmentGraphRef]string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[segmentGraphRef]string, len(s.byGraph))
	maps.Copy(out, s.byGraph)
	return out, s.reported
}

// spawnBootSegmentPasses starts the two one-shot boot passes, in the order their
// dependency runs: the coverage RECONCILE (which heals) and then the balance REPORT
// (which states what is left). Both are goroutines, so neither delay is awaited on
// the wiring path.
//
// NEITHER RUNS SYNCHRONOUSLY, and for the reconcile that is a deliberate reversal.
// The synchronous startup reconcile was removed because, with the L2-first load (the
// resident set is imported from the L2 disk cache server-independently before the MCP
// bind), boot no longer needs a server round trip to be searchable — running the
// all-graphs server reconcile on the bind path only coupled first-search readiness to
// a slow or down server.
//
// THE RECONCILE ONE-SHOT IS STILL REQUIRED, NOT A NICETY. runSegmentReconcileLoop's
// first tick fires only at segmentReconcileInterval (5min) because it selects on
// ticker.C with no immediate first iteration, so with the synchronous reconcile gone
// AND no per-search degeneracy backstop, a graph genuinely degenerate after the load —
// a cold or partial L2 on this machine — would otherwise sit degenerate for up to 5min
// post-restart. Its delay closes that heal gap while firing well after readiness
// latches, so it never blocks the MCP listener bind.
//
// THE BALANCE REPORT IS A SEPARATE GOROUTINE rather than a tail on the reconcile,
// because the two answer different questions on different delays and chaining them
// would make the report's timing an artifact of the heal's duration.
func (c *client) spawnBootSegmentPasses(ctx context.Context) {
	go c.bootDelayReconcile(ctx)
	go c.bootDelayBalanceReport(ctx)
}

// bootDelayBalanceReport runs ONE balance evaluation per segment-bearing graph
// segmentBalanceBootDelay after boot, OFF the bind / markPipelineReady critical
// path. Spawned with `go` from spawnBootSegmentPasses above, so the delay is awaited
// HERE. Exits promptly on ctx.Done (no leak).
//
// IT REPORTS AND DOES NOT ARM. No reap and no rebuild is invoked from here, and that
// is a deliberate division rather than an omission: the repair arms are driven by the
// quiescence edge, which knows the pipeline has drained, and boot knows no such
// thing. A rebuild fired here would act on a pool whose in-flight work has not
// landed, which is precisely the "report a corpus merely in flight as short" mistake
// the quiescence gate exists to prevent. What boot CAN say honestly is what the pool
// looks like right now, so that is all it says.
//
// THE EVALUATION LOADS THE POOL, which is what makes a boot-time reading meaningful
// at all. evaluateArmBalance reads its resident operands through the LOADING probe
// (LoadSegmentDocCounts), so the L2-resident set is imported before it is counted; a
// non-loading read at boot would report every un-searched graph as holding nothing
// and turn this whole pass into a corpus-scale false alarm.
func (c *client) bootDelayBalanceReport(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(segmentBalanceBootDelay):
	}
	c.reportStartupBalance(ctx)
}

// reportStartupBalance is the pass body, separated from its delay so a test drives
// the behaviour without waiting on a clock.
func (c *client) reportStartupBalance(ctx context.Context) {
	if c.segmentMgr == nil {
		return // headless / degraded — no segment engine to evaluate.
	}
	// The same query-origin stamp the quiescence closure applies, so this pass's
	// coverage reads are attributable to the segment arm rather than arriving
	// unattributed.
	ctx = graphclient.WithOperation(ctx, graphclient.OpSegmentHeal)

	for _, g := range c.segmentBearingGraphs() {
		if ctx.Err() != nil {
			return
		}
		b := c.evaluateArmBalance(ctx, g.gt, g.name)
		c.startupBalance.record(g, b.String())
		logStartupBalance(g.gt, g.name, b)
	}
}

// logStartupBalance emits the boot verdict at the severity its content earns. A
// balanced pool is Debug because it is the steady state and a line per graph per boot
// would train a reader past the ones that matter; an imbalance is Error because it is
// a defect nobody has yet been told about; and a refusal is Info rather than silence,
// because "could not measure" is a different fact from "measured healthy" and
// dropping it is how a check that never ran reads as a passing one.
func logStartupBalance(gt kgtypes.GraphType, name string, b armBalance) {
	switch b.verdict {
	case armBalanced:
		slog.Debug("bootstrap: startup segment balance", "graph_type", gt, "name", name,
			"verdict", b.String())
	case armNotEvaluated:
		slog.Info("bootstrap: startup segment balance not evaluated",
			"graph_type", gt, "name", name, "verdict", b.String())
	default:
		slog.Error("bootstrap: startup segment balance is IMBALANCED — this pool was already "+
			"pathological when the daemon started; no reap and no rebuild is driven from "+
			"boot, the existing arms own the repair",
			"graph_type", gt, "name", name, "verdict", b.String())
	}
}

// THE STATUS SURFACE FINDS THIS METHOD BY TYPE ASSERTION, not by an interface the
// compiler checks across the two packages — tools.startupBalanceReader is
// unexported, and bootstrap is the side that imports tools, so there is no
// declaration both halves can name. A drift in this signature would therefore not
// break the build; it would make the assertion in tools.renderStartupBalance stop
// matching and the whole section silently disappear from manage(status). This
// assertion is what turns that silence into a compile error on THIS side, and its
// shape must stay byte-identical to the interface in
// cmd/knowledge/internal/tools/manage_status_startup_balance.go.
var _ interface {
	StartupBalanceVerdicts() (map[string]string, bool)
} = (*client)(nil)

// StartupBalanceVerdicts renders the boot verdicts for the status surface, keyed by
// the "<graph_type>/<name>" label the coverage table uses. The second return reports
// whether the boot pass has RUN, which an empty map cannot express on its own.
func (c *client) StartupBalanceVerdicts() (map[string]string, bool) {
	if c == nil {
		return nil, false
	}
	byGraph, reported := c.startupBalance.snapshot()
	out := make(map[string]string, len(byGraph))
	for g, v := range byGraph {
		out[string(g.gt)+"/"+g.name] = v
	}
	return out, reported
}
