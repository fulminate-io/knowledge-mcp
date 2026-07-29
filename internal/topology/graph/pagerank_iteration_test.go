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

// pagerank_iteration_test.go covers both PageRank kernels — the unweighted
// runFullPowerIteration and the weighted runWeightedPowerIteration — against
// the same two bars: equivalence with gonum's network.PageRankSparse on inputs
// where gonum terminates, and the termination contract the library call cannot
// offer (iteration cap with a named error, in-loop cancellation).
//
// Parity tolerances: gonum seeds its start vector from an unseeded
// math/rand/v2, so its own output varies run to run at the tolerance level
// while ours starts deterministically at uniform 1/n. Both vectors sum to 1 and
// PageRank's fixed point is unique for damping < 1, so the two agree — but the
// bar has to leave room for gonum's seed. gonum runs at gonumParityTolerance
// (1e-12), four orders tighter than the comparison bar (parityBar, 1e-8). A
// failure at that separation is a kernel defect, not a seed.

const (
	gonumParityTolerance = 1e-12
	parityBar            = 1e-8
)

// buildUnweightedKernelGraph materializes a fixture the way PageRankAnalyzer
// does — through NewGonumGraphUnweighted, which forces every edge to weight 1.
func buildUnweightedKernelGraph(t *testing.T, f *graphFixture) *foundation.GonumGraph {
	t.Helper()
	g, err := foundation.NewGonumGraphUnweighted(newTestCtx(t), f, kgtypes.GraphKnowledge, "default", nil)
	if err != nil {
		t.Fatalf("NewGonumGraphUnweighted: %v", err)
	}
	return g
}

// buildWeightedFixture returns a 5-node graph with deliberately lopsided edge
// weights, so weighted and unweighted PageRank cannot agree by accident.
// finding-4 has no out-edges, which exercises the dangling (zero out-weight)
// branch of the weighted row builder.
func buildWeightedFixture() *graphFixture {
	f := newGraphFixture()
	for i := range 5 {
		f.AddNodeFull(findingID(i), findingID(i), kgtypes.NodeFinding, "", nil)
	}
	f.AddWeightedEdge(findingID(0), findingID(1), kgtypes.EdgeRelatesTo, 9)
	f.AddWeightedEdge(findingID(0), findingID(2), kgtypes.EdgeRelatesTo, 1)
	f.AddWeightedEdge(findingID(1), findingID(3), kgtypes.EdgeRelatesTo, 4)
	f.AddWeightedEdge(findingID(1), findingID(4), kgtypes.EdgeRelatesTo, 3)
	f.AddWeightedEdge(findingID(2), findingID(3), kgtypes.EdgeRelatesTo, 1)
	f.AddWeightedEdge(findingID(3), findingID(0), kgtypes.EdgeRelatesTo, 2)
	return f
}

// assertScoresMatch compares two int64-keyed score maps entry for entry.
func assertScoresMatch(t *testing.T, got, want map[int64]float64, bar float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("score count = %d, want %d", len(got), len(want))
	}
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			t.Fatalf("node %d missing from kernel scores", id)
		}
		if math.Abs(g-w) > bar {
			t.Errorf("node %d: kernel %g, gonum %g (delta %g > %g)", id, g, w, math.Abs(g-w), bar)
		}
	}
}

// TestRunFullPowerIteration_MatchesGonum pins the unweighted kernel to gonum's
// network.PageRankSparse over a unit-weight graph. This equivalence has never
// been under test — the regression pin the original port specified did not
// survive the client-side relocation.
func TestRunFullPowerIteration_MatchesGonum(t *testing.T) {
	g := buildUnweightedKernelGraph(t, buildStarFixture())

	got, err := runFullPowerIteration(newTestCtx(t), g, 0.85, gonumParityTolerance, dfprMaxIterations)
	if err != nil {
		t.Fatalf("runFullPowerIteration: %v", err)
	}
	want := network.PageRankSparse(g.WeightedDirectedGraph, 0.85, gonumParityTolerance)
	assertScoresMatch(t, got, want, parityBar)
}

// TestRunWeightedPowerIteration_MatchesGonum pins the weighted kernel to the
// weighted branch of gonum's PageRankSparse (which is what actually runs for a
// simple.WeightedDirectedGraph) over a graph whose weights are not uniform.
func TestRunWeightedPowerIteration_MatchesGonum(t *testing.T) {
	g := buildKernelGraph(t, buildWeightedFixture())

	got, err := runWeightedPowerIteration(newTestCtx(t), g, 0.85, gonumParityTolerance, dfprMaxIterations)
	if err != nil {
		t.Fatalf("runWeightedPowerIteration: %v", err)
	}
	want := network.PageRankSparse(g.WeightedDirectedGraph, 0.85, gonumParityTolerance)
	assertScoresMatch(t, got, want, parityBar)
}

// TestRunWeightedPowerIteration_WeightsChangeScores guards against the weighted
// analyzer silently running the unweighted builder: on the lopsided fixture the
// two kernels must disagree. Without this, a parity test over a unit-weight
// graph would pass with the weight signal entirely dropped.
func TestRunWeightedPowerIteration_WeightsChangeScores(t *testing.T) {
	f := buildWeightedFixture()
	weighted, err := runWeightedPowerIteration(newTestCtx(t), buildKernelGraph(t, f), 0.85, 1e-10, dfprMaxIterations)
	if err != nil {
		t.Fatalf("weighted: %v", err)
	}
	unweighted, err := runFullPowerIteration(newTestCtx(t), buildUnweightedKernelGraph(t, f), 0.85, 1e-10, dfprMaxIterations)
	if err != nil {
		t.Fatalf("unweighted: %v", err)
	}

	differs := false
	for id, w := range weighted {
		if math.Abs(w-unweighted[id]) > 1e-6 {
			differs = true
		}
	}
	if !differs {
		t.Errorf("weighted and unweighted scores agree on a lopsided-weight graph: %v vs %v — the weight signal is being dropped", weighted, unweighted)
	}
}

// TestPowerIteration_NonConvergenceReturnsError verifies the iteration cap on
// both kernels: a run that cannot reach tolerance within maxIter sweeps returns
// an error naming the analyzer and the iteration count. Before this change the
// unweighted kernel returned an unconverged vector with no error at all, and
// the weighted path had no cap to reach.
func TestPowerIteration_NonConvergenceReturnsError(t *testing.T) {
	t.Run("unweighted", func(t *testing.T) {
		g := buildUnweightedKernelGraph(t, buildStarFixture())
		_, err := runFullPowerIteration(newTestCtx(t), g, 0.85, 1e-12, 1)
		assertNonConvergence(t, err, "pagerank")
	})
	t.Run("weighted", func(t *testing.T) {
		g := buildKernelGraph(t, buildWeightedFixture())
		_, err := runWeightedPowerIteration(newTestCtx(t), g, 0.85, 1e-12, 1)
		assertNonConvergence(t, err, "pagerank_weighted")
	})
}

// assertNonConvergence checks the cap error names its analyzer and its count.
func assertNonConvergence(t *testing.T, err error, name string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a convergence-failure error when the cap is reached")
	}
	for _, want := range []string{name, "did not converge", "1 iterations"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// TestPowerIteration_CancellationInterruptsIteration verifies ctx is honored
// from inside the loop on both kernels, not only at the analyzer boundary.
// cancelAfterFirstCheckCtx (hits_iteration_test.go) reports success once and
// cancellation thereafter, so a kernel that only checked ctx before the loop
// would consume the nil and run to convergence.
func TestPowerIteration_CancellationInterruptsIteration(t *testing.T) {
	t.Run("unweighted", func(t *testing.T) {
		g := buildUnweightedKernelGraph(t, buildStarFixture())
		ctx := &cancelAfterFirstCheckCtx{Context: context.Background()}
		_, err := runFullPowerIteration(ctx, g, 0.85, 1e-12, dfprMaxIterations)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled from the in-loop check", err)
		}
		if ctx.checks < 2 {
			t.Errorf("ctx checked %d times; an in-loop check must run on every sweep", ctx.checks)
		}
	})
	t.Run("weighted", func(t *testing.T) {
		g := buildKernelGraph(t, buildWeightedFixture())
		ctx := &cancelAfterFirstCheckCtx{Context: context.Background()}
		_, err := runWeightedPowerIteration(ctx, g, 0.85, 1e-12, dfprMaxIterations)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled from the in-loop check", err)
		}
		if ctx.checks < 2 {
			t.Errorf("ctx checked %d times; an in-loop check must run on every sweep", ctx.checks)
		}
	})
}

// TestPowerIteration_Edgeless documents why neither kernel needs the edgeless
// short-circuit HITS requires: PageRank divides by nothing, so a graph with no
// edges is all-dangling and settles on the uniform 1/n vector in a single
// sweep. maxIter is set to 2 — enough for the converged sweep plus its check,
// far short of anything that could mask a spin.
func TestPowerIteration_Edgeless(t *testing.T) {
	t.Run("unweighted", func(t *testing.T) {
		g := buildUnweightedKernelGraph(t, buildEdgelessFixture())
		scores, err := runFullPowerIteration(newTestCtx(t), g, 0.85, 1e-8, 2)
		if err != nil {
			t.Fatalf("edgeless unweighted: %v", err)
		}
		assertUniform(t, scores)
	})
	t.Run("weighted", func(t *testing.T) {
		g := buildKernelGraph(t, buildEdgelessFixture())
		scores, err := runWeightedPowerIteration(newTestCtx(t), g, 0.85, 1e-8, 2)
		if err != nil {
			t.Fatalf("edgeless weighted: %v", err)
		}
		assertUniform(t, scores)
	})
}

// assertUniform checks every node holds an equal share of the unit mass.
func assertUniform(t *testing.T, scores map[int64]float64) {
	t.Helper()
	if len(scores) != 2 {
		t.Fatalf("expected a score per node, got %d", len(scores))
	}
	want := 1.0 / float64(len(scores))
	for id, s := range scores {
		if math.Abs(s-want) > 1e-12 {
			t.Errorf("node %d scored %g, want the uniform %g on an edgeless graph", id, s, want)
		}
	}
}

// TestPowerIteration_ZeroNodes verifies the empty-graph short-circuit returns
// an empty result rather than erroring or entering the iteration.
func TestPowerIteration_ZeroNodes(t *testing.T) {
	g := buildKernelGraph(t, newGraphFixture())

	unweighted, err := runFullPowerIteration(newTestCtx(t), g, 0.85, 1e-8, 0)
	if err != nil || len(unweighted) != 0 {
		t.Errorf("unweighted zero-node run = (%v, %v), want (empty, nil)", unweighted, err)
	}
	weighted, err := runWeightedPowerIteration(newTestCtx(t), g, 0.85, 1e-8, 0)
	if err != nil || len(weighted) != 0 {
		t.Errorf("weighted zero-node run = (%v, %v), want (empty, nil)", weighted, err)
	}
}

// TestPageRankAnalyzers_CancelledRequest verifies a cancelled request fails
// fast through both analyzer surfaces rather than running the iteration out.
func TestPageRankAnalyzers_CancelledRequest(t *testing.T) {
	for _, a := range []foundation.Analyzer{PageRankAnalyzer{}, WeightedPageRankAnalyzer{}} {
		t.Run(a.Name(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			start := time.Now()
			_, err := runWithDeadline(t, ctx, a, starReq(buildStarFixture(), 20))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("cancelled run took %s, want an immediate return", elapsed)
			}
		})
	}
}
