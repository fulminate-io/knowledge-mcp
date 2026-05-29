// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TrustMatrix is a row-stochastic matrix encoding how much each thought
// trusts (listens to) each other thought's valence/magnitude. Stored
// per-row in sparse form (typical nnz/row 4-10 vs N=5066+).
type TrustMatrix struct {
	IDs     []string
	IDIndex map[string]int
	Rows    [][]SparseEntry
}

// SparseEntry is one (column, value) pair in a TrustMatrix row.
type SparseEntry struct {
	Col int
	Val float64
}

// PropagationResult summarizes one run of the propagation engine.
type PropagationResult struct {
	ThoughtsProcessed int
	Components        int
	Iterations        int
	Converged         bool
	ValenceChanges    map[string]float64
	MagnitudeChanges  map[string]float64
}

const (
	defaultMaxIterations = 100
	defaultEpsilon       = 1e-6
	magnitudeDecay       = 0.7
)

// BuildTrustMatrix constructs a row-stochastic trust matrix for the
// given thoughts. Issues EXACTLY ONE gc.Call("thoughts",
// {operation:"adjacency", thought_ids: thoughtIDs}) for the cross-edge
// fanout + ONE gc.Call("thoughts", {operation:"charges_for"}) for
// self-trust derivation. fillSparseRows operates entirely from those
// two prebuilt maps.
func BuildTrustMatrix(ctx context.Context, gc Caller, thoughtIDs []string) (TrustMatrix, error) {
	n := len(thoughtIDs)
	if n == 0 {
		return TrustMatrix{}, nil
	}
	idIndex := make(map[string]int, n)
	for i, id := range thoughtIDs {
		idIndex[id] = i
	}
	_, adj, err := fetchAdjacency(ctx, gc, "all", thoughtIDs)
	if err != nil {
		return TrustMatrix{}, fmt.Errorf("thought: BuildTrustMatrix: adjacency: %w", err)
	}
	chargeMap := chargeMapForThoughts(ctx, gc, thoughtIDs)

	rows := fillSparseRows(thoughtIDs, idIndex, adj, chargeMap)
	normalizeSparseRows(rows)
	return TrustMatrix{IDs: thoughtIDs, IDIndex: idIndex, Rows: rows}, nil
}

// fillSparseRows builds the sparse trust-matrix rows from the prebuilt
// adjacency map + charge map. No wire calls.
func fillSparseRows(thoughtIDs []string, idIndex map[string]int, adj map[string][]string, chargeMap map[string][]*knowledgev1.Node) [][]SparseEntry {
	n := len(thoughtIDs)
	rows := make([][]SparseEntry, n)
	seen := make([]bool, n)
	for i, id := range thoughtIDs {
		for k := range seen {
			seen[k] = false
		}
		var row []SparseEntry
		for _, other := range adj[id] {
			j, ok := idIndex[other]
			if !ok || j == i || seen[j] {
				continue
			}
			seen[j] = true
			row = append(row, SparseEntry{Col: j, Val: 1.0})
		}
		row = append(row, SparseEntry{Col: i, Val: computePropertiesFromCharges(chargeMap[id]).SelfTrust})
		sortRowByCol(row)
		rows[i] = row
	}
	return rows
}

func sortRowByCol(row []SparseEntry) {
	for i := 1; i < len(row); i++ {
		for j := i; j > 0 && row[j-1].Col > row[j].Col; j-- {
			row[j-1], row[j] = row[j], row[j-1]
		}
	}
}

func normalizeSparseRows(rows [][]SparseEntry) {
	for i, row := range rows {
		rowSum := 0.0
		for _, e := range row {
			rowSum += e.Val
		}
		if rowSum > 0 {
			for j := range row {
				row[j].Val /= rowSum
			}
			continue
		}
		rows[i] = []SparseEntry{{Col: i, Val: 1.0}}
	}
}

// PropagateValence runs DeGroot iterations on valence until convergence.
func PropagateValence(matrix TrustMatrix, initialValence map[string]float64, maxIter int, epsilon float64) (map[string]float64, int, bool) {
	n := len(matrix.IDs)
	if n == 0 {
		return nil, 0, true
	}
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}
	if epsilon <= 0 {
		epsilon = defaultEpsilon
	}
	v := make([]float64, n)
	for i, id := range matrix.IDs {
		v[i] = initialValence[id]
	}
	vNext := make([]float64, n)
	var iterations int
	converged := false
	for iter := range maxIter {
		maxDelta := 0.0
		for i := range n {
			sum := 0.0
			for _, e := range matrix.Rows[i] {
				sum += e.Val * v[e.Col]
			}
			vNext[i] = sum
			delta := math.Abs(vNext[i] - v[i])
			if delta > maxDelta {
				maxDelta = delta
			}
		}
		copy(v, vNext)
		iterations = iter + 1
		if maxDelta < epsilon {
			converged = true
			break
		}
	}
	result := make(map[string]float64, n)
	for i, id := range matrix.IDs {
		result[id] = v[i]
	}
	return result, iterations, converged
}

// PropagateMagnitude uses decay-attenuated propagation.
func PropagateMagnitude(matrix TrustMatrix, localMagnitude map[string]float64, maxIter int, epsilon float64) (map[string]float64, int, bool) {
	n := len(matrix.IDs)
	if n == 0 {
		return nil, 0, true
	}
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}
	if epsilon <= 0 {
		epsilon = defaultEpsilon
	}
	m := make([]float64, n)
	local := make([]float64, n)
	for i, id := range matrix.IDs {
		m[i] = localMagnitude[id]
		local[i] = localMagnitude[id]
	}
	var iterations int
	converged := false
	for iter := range maxIter {
		maxDelta := 0.0
		for i := range n {
			maxNeighbor := 0.0
			for _, e := range matrix.Rows[i] {
				if e.Col == i {
					continue
				}
				if m[e.Col] > maxNeighbor {
					maxNeighbor = m[e.Col]
				}
			}
			newM := math.Max(local[i], magnitudeDecay*maxNeighbor)
			delta := newM - m[i]
			if delta > maxDelta {
				maxDelta = delta
			}
			m[i] = newM
		}
		iterations = iter + 1
		if maxDelta < epsilon {
			converged = true
			break
		}
	}
	result := make(map[string]float64, n)
	for i, id := range matrix.IDs {
		result[id] = m[i]
	}
	return result, iterations, converged
}

// ComputeInfluenceVector computes the left eigenvector of T via power
// iteration. Pure local computation.
func ComputeInfluenceVector(matrix TrustMatrix) map[string]float64 {
	n := len(matrix.IDs)
	if n == 0 {
		return nil
	}
	s := make([]float64, n)
	for i := range s {
		s[i] = 1.0 / float64(n)
	}
	sNext := make([]float64, n)
	for range defaultMaxIterations {
		for j := range sNext {
			sNext[j] = 0
		}
		for i := range n {
			si := s[i]
			for _, e := range matrix.Rows[i] {
				sNext[e.Col] += si * e.Val
			}
		}
		total := 0.0
		for _, v := range sNext {
			total += v
		}
		if total > 0 {
			for j := range sNext {
				sNext[j] /= total
			}
		}
		maxDelta := 0.0
		for i := range n {
			d := math.Abs(sNext[i] - s[i])
			if d > maxDelta {
				maxDelta = d
			}
		}
		copy(s, sNext)
		if maxDelta < defaultEpsilon {
			break
		}
	}
	result := make(map[string]float64, n)
	for i, id := range matrix.IDs {
		result[id] = s[i]
	}
	return result
}

// findConnectedComponents returns groups of thought IDs that are
// connected. Pure local computation over a prebuilt adjacency map.
func findConnectedComponents(thoughtIDs []string, adj map[string][]string) [][]string {
	idSet := make(map[string]bool, len(thoughtIDs))
	for _, id := range thoughtIDs {
		idSet[id] = true
	}
	visited := make(map[string]bool, len(thoughtIDs))
	var components [][]string
	for _, startID := range thoughtIDs {
		if visited[startID] {
			continue
		}
		var component []string
		queue := []string{startID}
		visited[startID] = true
		for len(queue) > 0 {
			curr := queue[0]
			queue = queue[1:]
			component = append(component, curr)
			for _, nid := range adj[curr] {
				if !visited[nid] && idSet[nid] {
					visited[nid] = true
					queue = append(queue, nid)
				}
			}
		}
		components = append(components, component)
	}
	return components
}

// RunPropagation executes full propagation across all thoughts.
// Single adjacency call up front; chargeMap fetched ONCE (T3 perf
// lock); per-component matrix build uses the prebuilt adj subset and
// chargeMap subset. Writeback is a SINGLE
// gc.Call("mutate", {operation:"bulk_update_metadata"}) at the end —
// O(1) RPCs regardless of N.
func RunPropagation(ctx context.Context, gc Caller, profile *PersonalityProfile, nodeByID map[string]*knowledgev1.Node) (PropagationResult, error) {
	nodeIDs, adj, err := fetchAdjacency(ctx, gc, "all", nil)
	if err != nil {
		return PropagationResult{}, fmt.Errorf("thought: RunPropagation: adjacency: %w", err)
	}
	if len(nodeIDs) == 0 {
		return PropagationResult{}, nil
	}
	chargeMap := chargeMapForThoughts(ctx, gc, nodeIDs)

	components := findConnectedComponents(nodeIDs, adj)
	result := PropagationResult{
		ThoughtsProcessed: len(nodeIDs),
		Components:        len(components),
		Converged:         true,
		ValenceChanges:    make(map[string]float64),
		MagnitudeChanges:  make(map[string]float64),
	}

	// Accumulate per-thought writeback rows; single bulk update at the
	// end (T2/T3 perf lock — no per-thought wire writes).
	var allUpdates []map[string]any
	for _, component := range components {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("propagation cancelled: %w", err)
		}
		matrix := buildComponentMatrix(component, adj, chargeMap, profile, nodeByID)

		// Hoist initial valence + local magnitude OUT of the per-thought
		// init loop — pure computation over the prebuilt chargeMap (T3
		// perf lock — no gc.Call in the per-id loop).
		initialValence := make(map[string]float64, len(component))
		localMagnitude := make(map[string]float64, len(component))
		for _, id := range component {
			props := computePropertiesFromCharges(chargeMap[id])
			initialValence[id] = props.Valence
			localMagnitude[id] = props.Magnitude
		}

		propagatedValence, vIter, vConverged := PropagateValence(matrix, initialValence, defaultMaxIterations, defaultEpsilon)
		result.Iterations += vIter
		if !vConverged {
			result.Converged = false
		}
		propagatedMagnitude, mIter, mConverged := PropagateMagnitude(matrix, localMagnitude, defaultMaxIterations, defaultEpsilon)
		result.Iterations += mIter
		if !mConverged {
			result.Converged = false
		}

		for _, id := range component {
			pv := propagatedValence[id]
			pm := propagatedMagnitude[id]
			result.ValenceChanges[id] = pv - initialValence[id]
			result.MagnitudeChanges[id] = pm - localMagnitude[id]
			allUpdates = append(allUpdates, map[string]any{
				"id": id,
				"metadata": map[string]string{
					"propagated_valence":   fmt.Sprintf("%.6f", pv),
					"propagated_magnitude": fmt.Sprintf("%.6f", pm),
				},
			})
		}
	}

	bulkPersistMetadata(ctx, gc, allUpdates)
	return result, nil
}

// buildComponentMatrix builds the TrustMatrix for one connected
// component, reusing the prebuilt adj subset and chargeMap.
// Personality scalars are applied if profile != nil.
func buildComponentMatrix(component []string, adj map[string][]string, chargeMap map[string][]*knowledgev1.Node, profile *PersonalityProfile, nodeByID map[string]*knowledgev1.Node) TrustMatrix {
	idIndex := make(map[string]int, len(component))
	for i, id := range component {
		idIndex[id] = i
	}
	rows := fillSparseRows(component, idIndex, adj, chargeMap)
	normalizeSparseRows(rows)
	matrix := TrustMatrix{IDs: component, IDIndex: idIndex, Rows: rows}
	if profile != nil {
		thoughtToCluster := make(map[string]string, len(component))
		for _, id := range component {
			if n, ok := nodeByID[id]; ok {
				if cid := kgtypes.Value(n, "cluster_id"); cid != "" {
					thoughtToCluster[id] = cid
				}
			}
		}
		n := len(matrix.IDs)
		for i := range n {
			applyPersonalityScalarsToRow(matrix, i, thoughtToCluster, *profile)
			renormalizeSparseRow(matrix.Rows[i])
		}
	}
	return matrix
}

// bulkPersistMetadata wraps the mutate(bulk_update_metadata) write, routed
// through the Execute carrier seam (executeViaEngine → MUTATION_KIND_UPDATE_ITEMS).
// Empty updates short-circuits; failures are logged-and-dropped.
func bulkPersistMetadata(ctx context.Context, gc Caller, updates []map[string]any) {
	if gc == nil || len(updates) == 0 {
		return
	}
	args, err := json.Marshal(map[string]any{
		"operation": "bulk_update_metadata",
		"updates":   updates,
	})
	if err != nil {
		slog.Warn("thought: bulkPersistMetadata: marshal failed", "err", err)
		return
	}
	// bulk_update_metadata lowers to MUTATION_KIND_UPDATE_ITEMS via the engine
	// (compileMutateBulkMetadata) and rides the Execute carrier seam.
	if _, err := executeViaEngine(ctx, gc, "mutate", args); err != nil {
		slog.Warn("thought: bulkPersistMetadata: execute failed", "err", err)
	}
}
