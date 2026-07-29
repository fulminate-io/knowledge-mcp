// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"gonum.org/v1/gonum/graph/network"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// hits_iteration_test.go covers the three termination guarantees of the HITS
// kernel — edgeless short-circuit, bounded iteration, in-loop cancellation —
// plus equivalence with gonum's network.HITS on inputs where gonum itself
// terminates. The analyzer-level view of the same guarantees lives in
// edgeless_test.go.

// buildKernelGraph materializes a fixture through the real adapter so the
// kernel sees exactly the graph an analyzer would hand it.
func buildKernelGraph(t *testing.T, f *graphFixture) *foundation.GonumGraph {
	t.Helper()
	g, err := foundation.NewGonumGraph(newTestCtx(t), f, kgtypes.GraphKnowledge, "default", nil)
	if err != nil {
		t.Fatalf("NewGonumGraph: %v", err)
	}
	return g
}

// TestHITSScores_Edgeless verifies the short-circuit: a graph with nodes and
// no edges returns a zero score for every node, without iterating. maxIter is
// set to zero so any entry into the loop would surface as the
// convergence-failure error instead of a result.
func TestHITSScores_Edgeless(t *testing.T) {
	g := buildKernelGraph(t, buildEdgelessFixture())

	scores, err := hitsScores(newTestCtx(t), g.WeightedDirectedGraph, 1e-8, 0, "hits_hubs")
	if err != nil {
		t.Fatalf("edgeless hitsScores: %v", err)
	}
	if len(scores) != 2 {
		t.Fatalf("expected a score pair per node, got %d", len(scores))
	}
	for id, ha := range scores {
		if ha.Hub != 0 || ha.Authority != 0 {
			t.Errorf("node %d scored hub=%g authority=%g, want zero on an edgeless graph", id, ha.Hub, ha.Authority)
		}
	}
}

// TestHITSScores_SelfLoopsOnlyIsEdgeless verifies the guard sits after the
// adapter, not before it: a graph whose only edges are self-loops arrives at
// the kernel with zero edges because the adapter drops them.
func TestHITSScores_SelfLoopsOnlyIsEdgeless(t *testing.T) {
	f := buildEdgelessFixture()
	f.AddEdge("finding-0", "finding-0", kgtypes.EdgeRelatesTo)
	f.AddEdge("finding-1", "finding-1", kgtypes.EdgeRelatesTo)
	g := buildKernelGraph(t, f)

	scores, err := hitsScores(newTestCtx(t), g.WeightedDirectedGraph, 1e-8, 0, "hits_hubs")
	if err != nil {
		t.Fatalf("self-loop-only hitsScores: %v", err)
	}
	for id, ha := range scores {
		if ha.Hub != 0 || ha.Authority != 0 {
			t.Errorf("node %d scored hub=%g authority=%g, want zero when only self-loops exist", id, ha.Hub, ha.Authority)
		}
	}
}

// TestHITSScores_MatchesGonum pins the kernel to gonum's network.HITS on a
// graph with edges, where gonum terminates. The two must agree to numerical
// precision — the kernel adds termination guarantees, not different math.
func TestHITSScores_MatchesGonum(t *testing.T) {
	g := buildKernelGraph(t, buildStarFixture())

	got, err := hitsScores(newTestCtx(t), g.WeightedDirectedGraph, 1e-8, hitsMaxIterations, "hits_hubs")
	if err != nil {
		t.Fatalf("hitsScores: %v", err)
	}
	want := network.HITS(g.WeightedDirectedGraph, 1e-8)
	if len(got) != len(want) {
		t.Fatalf("score count = %d, want %d", len(got), len(want))
	}
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			t.Fatalf("node %d missing from kernel scores", id)
		}
		if math.Abs(g.Hub-w.Hub) > 1e-9 || math.Abs(g.Authority-w.Authority) > 1e-9 {
			t.Errorf("node %d: kernel {hub %g auth %g}, gonum {hub %g auth %g}",
				id, g.Hub, g.Authority, w.Hub, w.Authority)
		}
	}
}

// TestHITSScores_NonConvergenceReturnsError verifies the iteration cap: a run
// that cannot reach tolerance within maxIter sweeps returns an error naming
// the analyzer and the iteration count instead of spinning.
func TestHITSScores_NonConvergenceReturnsError(t *testing.T) {
	g := buildKernelGraph(t, buildStarFixture())

	_, err := hitsScores(newTestCtx(t), g.WeightedDirectedGraph, 1e-12, 1, "hits_authorities")
	if err == nil {
		t.Fatal("expected a convergence-failure error when the cap is reached")
	}
	for _, want := range []string{"hits_authorities", "did not converge", "1 iterations"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// cancelAfterFirstCheckCtx reports no error on its first Err call and
// cancellation on every call after. A kernel that only checked ctx before the
// loop would consume the nil and then run to convergence; one that checks
// inside the loop returns the cancellation. This distinguishes the two
// without racing a timer against the iteration.
type cancelAfterFirstCheckCtx struct {
	context.Context
	checks int
}

func (c *cancelAfterFirstCheckCtx) Err() error {
	c.checks++
	if c.checks <= 1 {
		return nil
	}
	return context.Canceled
}

// TestHITSScores_CancellationInterruptsIteration verifies ctx is honored
// from inside the loop, not only at the boundaries.
func TestHITSScores_CancellationInterruptsIteration(t *testing.T) {
	g := buildKernelGraph(t, buildStarFixture())
	ctx := &cancelAfterFirstCheckCtx{Context: context.Background()}

	_, err := hitsScores(ctx, g.WeightedDirectedGraph, 1e-8, hitsMaxIterations, "hits_hubs")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled from the in-loop check", err)
	}
	if ctx.checks < 2 {
		t.Errorf("ctx checked %d times; an in-loop check must run on every sweep", ctx.checks)
	}
}

// TestHITSAnalyzers_EdgelessProduceNoFindings is the analyzer-level contract
// for an edgeless graph: both HITS analyzers return promptly with no error
// and no findings, because a node with a zero hub/authority score is not a
// high-hub or high-authority node.
func TestHITSAnalyzers_EdgelessProduceNoFindings(t *testing.T) {
	for _, a := range []foundation.Analyzer{HITSHubAnalyzer{}, HITSAuthorityAnalyzer{}} {
		t.Run(a.Name(), func(t *testing.T) {
			f := buildEdgelessFixture()
			findings, err := runWithDeadline(t, newTestCtx(t), a, starReq(f, 20))
			if err != nil {
				t.Fatalf("%s on an edgeless graph: %v", a.Name(), err)
			}
			if len(findings) != 0 {
				t.Errorf("%s produced %d findings on an edgeless graph, want none", a.Name(), len(findings))
			}
		})
	}
}

// TestHITSAnalyzers_CancelledRequest verifies a cancelled request fails fast
// through the analyzer surface rather than running the iteration to
// completion.
func TestHITSAnalyzers_CancelledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := buildStarFixture()
	start := time.Now()
	_, err := runWithDeadline(t, ctx, HITSHubAnalyzer{}, starReq(f, 20))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("cancelled run took %s, want an immediate return", elapsed)
	}
}
