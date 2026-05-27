// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"sync"

	"gonum.org/v1/gonum/graph/network"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// betweenness_sampled.go implements the sampled Brandes kernel (Bader &
// Madduri 2007) plus the runSampled + runPerPackage entry points. The
// single-source BFS + accumulation loop is reimplemented inline because
// gonum's brandes() at graph/network/betweenness.go:91-144 is unexported.
// The estimator picks k uniform sources, runs single-source Brandes
// from each, and scales the result by V/k — unbiased with variance
// shrinking as sqrt(k). Sample variance of per-source contributions
// is surfaced as a scalar metric for estimator-quality reasoning.

// sampledBrandes runs the Bader-Madduri sampled Brandes estimator.
// Returns (scaled betweenness per int64 node ID, sample variance).
// rng must be non-nil; k is clamped to [1, N] internally.
//
// All per-source BFS scratch buffers are allocated once and reset
// between sources — the hot path is entirely slice-backed, no
// map operations. Only the final cb→map conversion at the end touches
// a map.
func sampledBrandes(ctx context.Context, g *foundation.GonumGraph, k int, rng *rand.Rand) (map[int64]float64, float64) {
	allNodeIDs := collectNodeIDs(g)
	n := len(allNodeIDs)
	if n == 0 || k <= 0 {
		return nil, 0
	}
	if k > n {
		k = n
	}

	rng.Shuffle(n, func(i, j int) {
		allNodeIDs[i], allNodeIDs[j] = allNodeIDs[j], allNodeIDs[i]
	})
	sources := allNodeIDs[:k]

	// Parallelize the 200-ish independent BFS runs across workers.
	// Each worker owns its own scratch buffer and a private cb slice;
	// the main goroutine sums per-worker cb slices at the end so
	// there's no contention on the shared accumulator.
	workers := max(min(runtime.GOMAXPROCS(0), k), 1)
	workerCB := make([][]float64, workers)
	perSourceSum := make([]float64, k)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			scratch := newBrandesScratch(n)
			cb := make([]float64, n)
			for i := w; i < k; i += workers {
				if ctx.Err() != nil {
					break
				}
				before := sliceSum(cb)
				singleSourceBrandes(g, int32(sources[i]), cb, scratch)
				perSourceSum[i] = sliceSum(cb) - before
			}
			workerCB[w] = cb
		})
	}
	wg.Wait()

	// Merge worker cb slices into a single accumulator.
	cb := make([]float64, n)
	for _, wcb := range workerCB {
		for i, v := range wcb {
			cb[i] += v
		}
	}

	scale := float64(n) / float64(k)
	out := make(map[int64]float64, n)
	for id, score := range cb {
		if score != 0 {
			out[int64(id)] = score * scale
		}
	}
	return out, sampleVariance(perSourceSum)
}

// collectNodeIDs snapshots int64 IDs into a sorted slice. Sorting is
// essential for reproducibility: gonum's WeightedDirectedGraph.Nodes()
// iterates its underlying map non-deterministically, which would cause
// the downstream shuffle to draw different sources on each run.
func collectNodeIDs(g *foundation.GonumGraph) []int64 {
	nodes := g.Nodes()
	out := make([]int64, 0, nodes.Len())
	for nodes.Next() {
		out = append(out, nodes.Node().ID())
	}
	slices.Sort(out)
	return out
}

// brandesScratch holds reusable slice-backed BFS state for the Brandes
// kernel. Every field is indexed by gonum node ID (int32) — gonum
// assigns contiguous 0..N-1 IDs in our adapter, so direct indexing is
// safe and far cheaper than map[int64] lookups.
//
// Lifecycle: allocate once per sampledBrandes call, reset between
// sources via resetTouched (which walks the stack — the set of nodes
// visited during the previous BFS — and clears only those entries).
type brandesScratch struct {
	sigma []float64
	dist  []int32 // -1 = unvisited
	pred  [][]int32
	delta []float64
	stack []int32
	queue []int32
}

// newBrandesScratch allocates scratch buffers sized for n nodes and
// initializes dist to all -1 (unvisited).
func newBrandesScratch(n int) *brandesScratch {
	s := &brandesScratch{
		sigma: make([]float64, n),
		dist:  make([]int32, n),
		pred:  make([][]int32, n),
		delta: make([]float64, n),
		stack: make([]int32, 0, 64),
		queue: make([]int32, 0, 64),
	}
	for i := range s.dist {
		s.dist[i] = -1
	}
	return s
}

// resetTouched walks the previous run's stack and clears scratch
// entries for each visited node. Nodes not on the stack retain their
// zero/reset state from newBrandesScratch. Called once per singleSource
// call before seeding the new source.
func (s *brandesScratch) resetTouched() {
	for _, v := range s.stack {
		s.sigma[v] = 0
		s.dist[v] = -1
		if len(s.pred[v]) > 0 {
			s.pred[v] = s.pred[v][:0]
		}
		s.delta[v] = 0
	}
	s.stack = s.stack[:0]
	s.queue = s.queue[:0]
}

// singleSourceBrandes runs one BFS from source s and accumulates the
// per-source dependency scores into cb in place. Same logic as gonum's
// private brandes() at graph/network/betweenness.go:91-144, inlined
// because the gonum helper is unexported.
//
// Uses the caller-supplied scratch buffers exclusively — no allocation
// in the hot path, no map operations. Gonum IDs are assumed to be in
// [0, n) which matches the adapter's sequential ID assignment.
func singleSourceBrandes(g *foundation.GonumGraph, s int32, cb []float64, sc *brandesScratch) {
	if _, ok := g.StringID(int64(s)); !ok {
		return
	}
	sc.resetTouched()
	sc.sigma[s] = 1
	sc.dist[s] = 0
	sc.queue = append(sc.queue, s)

	// BFS with a head-index queue — queue[1:] slice reslicing would
	// force a copy under the hood as the array grows, and more
	// importantly it confuses append() because the next push walks
	// past the old head. A plain head pointer is O(1) per pop.
	head := 0
	for head < len(sc.queue) {
		v := sc.queue[head]
		head++
		sc.stack = append(sc.stack, v)
		to := g.From(int64(v))
		for to.Next() {
			w := int32(to.Node().ID())
			if sc.dist[w] < 0 {
				sc.dist[w] = sc.dist[v] + 1
				sc.queue = append(sc.queue, w)
			}
			if sc.dist[w] == sc.dist[v]+1 {
				sc.sigma[w] += sc.sigma[v]
				sc.pred[w] = append(sc.pred[w], v)
			}
		}
	}

	for i := len(sc.stack) - 1; i >= 0; i-- {
		w := sc.stack[i]
		sigmaW := sc.sigma[w]
		if sigmaW == 0 {
			continue
		}
		coef := (1 + sc.delta[w]) / sigmaW
		for _, v := range sc.pred[w] {
			sc.delta[v] += sc.sigma[v] * coef
		}
		if w != s {
			cb[w] += sc.delta[w]
		}
	}
}

// sliceSum totals every value in a []float64.
func sliceSum(xs []float64) float64 {
	var total float64
	for _, v := range xs {
		total += v
	}
	return total
}

// sampleVariance returns the classical unbiased sample variance
// s^2 = (1/(n-1)) * sum_i (x_i - xbar)^2. Returns 0 when len(xs) < 2.
func sampleVariance(xs []float64) float64 {
	n := len(xs)
	if n < 2 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(n)
	var sq float64
	for _, x := range xs {
		d := x - mean
		sq += d * d
	}
	if math.IsNaN(sq) || math.IsInf(sq, 0) {
		return 0
	}
	return sq / float64(n-1)
}

// runSampled runs sampled Brandes via sampledBrandes and emits top-K
// findings. seed comes from req.Extra["seed"] or a deterministic (V,E)
// fingerprint; sample size comes from req.Extra["sample_size"] or the
// adaptive default min(maxSampleSize, V/50), clamped to [1, V].
func (a SampledBetweennessAnalyzer) runSampled(ctx context.Context, req foundation.Request, g *foundation.GonumGraph) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/betweenness: %w", err)
	}
	v := g.Nodes().Len()
	e := g.Edges().Len()
	k := pickSampleSize(req, v)
	seed := pickSeed(req, v, e)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))

	scores, variance := sampledBrandes(ctx, g, k, rng)
	if len(scores) == 0 {
		return nil, nil
	}
	items, allScores := collectBetweennessItems(g, scores)
	return buildBetweennessFindings(ctx, req, items, allScores, findingsBuildConfig{
		mode:           modeSampled,
		sampleSize:     k,
		sampleVariance: variance,
	}), nil
}

// pickSampleSize reads req.Extra["sample_size"] or falls back to the
// adaptive default min(maxSampleSize, V/50) with a floor of defaultSampleSize.
func pickSampleSize(req foundation.Request, v int) int {
	adaptive := min(maxSampleSize, max(v/50, defaultSampleSize))
	raw, ok := req.Extra["sample_size"]
	if !ok {
		return clampSampleSize(adaptive, v)
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 || parsed > v {
		return clampSampleSize(adaptive, v)
	}
	return clampSampleSize(parsed, v)
}

// clampSampleSize bounds k to [1, V].
func clampSampleSize(k, v int) int {
	if k < 1 {
		return 1
	}
	if k > v {
		return v
	}
	return k
}

// pickSeed reads req.Extra["seed"] or derives a deterministic seed from
// the graph's (V, E) fingerprint (stable across runs on unchanged graph).
func pickSeed(req foundation.Request, v, e int) uint64 {
	if raw, ok := req.Extra["seed"]; ok {
		if parsed, err := strconv.ParseUint(raw, 10, 64); err == nil {
			return parsed
		}
	}
	return uint64(v)*0x9e3779b97f4a7c15 + uint64(e)*0xbf58476d1ce4e5b9 + 1
}

// runPerPackage computes exact betweenness per NodePackage subgraph,
// merges the results into a single global top-K, and emits one Finding
// per surfaced node. Packages with fewer than 3 contained nodes are
// skipped. Titles carry "Bridge in <pkg>: <node>" for context.
func (a SampledBetweennessAnalyzer) runPerPackage(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/betweenness: %w", err)
	}
	packages, err := foundation.FetchNodesByType(ctx, req.Caller, req.Graph, req.Name, kgtypes.NodePackage)
	if err != nil {
		return nil, fmt.Errorf("topology/betweenness: query packages: %w", err)
	}

	all := make([]foundation.Finding, 0, len(packages)*2)
	for i := range packages {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/betweenness: %w", err)
		}
		pkg := packages[i]
		pkgFindings, perr := runPerPackageOne(ctx, req, pkg)
		if perr != nil {
			return nil, perr
		}
		all = append(all, pkgFindings...)
	}
	k := req.TopK
	if k <= 0 {
		k = defaultTopK
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].Metrics["betweenness"] > all[j].Metrics["betweenness"]
	})
	if len(all) > k {
		all = all[:k]
	}
	return all, nil
}

// runPerPackageOne computes exact betweenness for one package's
// contained-node subgraph via NewGonumGraphUnweighted and returns the
// per-node findings for that package. The contained-node set is read in
// ONE bulk wire edge fetch over the package node, filtered to forward
// CONTAINS edges.
func runPerPackageOne(ctx context.Context, req foundation.Request, pkg *knowledgev1.Node) ([]foundation.Finding, error) {
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, []string{pkg.Id}, []kgtypes.EdgeType{kgtypes.EdgeContains})
	if err != nil {
		return nil, fmt.Errorf("topology/betweenness: package contains %s: %w", pkg.Id, err)
	}
	contained := make(map[string]bool, len(edges))
	for i := range edges {
		e := &edges[i]
		// Forward CONTAINS: package → contained node.
		if e.FromId == pkg.Id && e.ToId != "" {
			contained[e.ToId] = true
		}
	}
	if len(contained) < 3 {
		return nil, nil
	}
	subset := func(n *knowledgev1.Node) bool { return contained[n.Id] }
	subG, err := foundation.NewGonumGraphUnweighted(ctx, req.Caller, req.Graph, req.Name, subset)
	if err != nil {
		return nil, fmt.Errorf("topology/betweenness: build pkg subgraph %s: %w", pkg.Id, err)
	}
	if subG.Nodes().Len() < 3 {
		return nil, nil
	}
	scores := network.Betweenness(subG.WeightedDirectedGraph)
	if len(scores) == 0 {
		return nil, nil
	}
	items, allScores := collectBetweennessItems(subG, scores)
	pkgDisplay := pkg.SymbolName
	if pkgDisplay == "" {
		pkgDisplay = pkg.Id
	}
	return buildBetweennessFindings(ctx, req, items, allScores, findingsBuildConfig{
		mode:       modePerPackage,
		pkgDisplay: pkgDisplay,
		pkgSize:    subG.Nodes().Len(),
	}), nil
}
