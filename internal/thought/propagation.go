// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// PropagationResult summarizes one run of the propagation engine.
type PropagationResult struct {
	ThoughtsProcessed int
	Components        int
	Iterations        int
	// Converged is DERIVED as len(NonConverged)==0 — it now means "every recomputed
	// component converged", not the old global AND-flag. Kept for render/log
	// backward-compat; the per-component detail lives in NonConverged.
	Converged           bool
	ComponentsConverged int                     // components that converged (both valence + magnitude).
	NonConverged        []NonConvergedComponent // the worst non-converged components by residual (capped).
	NonConvergedOmitted int                     // non-converged components beyond the cap, not listed.
	ValenceChanges      map[string]float64
	MagnitudeChanges    map[string]float64
}

// NonConvergedComponent reports one connected component that did NOT converge
// within the iteration cap, with its size and the final valence/magnitude
// residuals (the leftover gap at cap).
type NonConvergedComponent struct {
	Size              int
	ValenceResidual   float64
	MagnitudeResidual float64
}

// nonConvergedReportCap bounds how many non-converged components RunPropagationScoped
// lists (the worst-K by residual); the rest are summarized via NonConvergedOmitted.
const nonConvergedReportCap = 5

// residualBand is a node's per-component writeback band for the two propagated_*
// keys. A converged component floors both at writebackDeadband; a non-converged
// component widens them to its own oscillation amplitude so residual jitter below
// observable precision stops re-writing every tick.
type residualBand struct {
	valence   float64
	magnitude float64
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

// dirtyComponentClosure returns the union of every connected component that
// contains at least one seed node, preserving each component's member order.
// Pure local computation over the precomputed [][]string partition — no wire, no
// DB. Components with no seed member are excluded; their converged DeGroot values
// are provably invariant between ticks (block-diagonal per-component trust
// matrix), so skipping them is EXACT, not approximate.
//
// JOIN-SAFE BOUNDARY: callers build `components` via findConnectedComponents over
// the NEW (post-edge) adjacency, so a bridging edge that fused two previously
// separate components yields ONE component containing both seed endpoints. The
// closure of either endpoint then pulls in the whole fused component — the
// bridging-edge JOIN is captured structurally, with no special-casing here.
func dirtyComponentClosure(seed map[string]bool, components [][]string) []string {
	var closure []string
	for _, component := range closureComponents(seed, components) {
		closure = append(closure, component...)
	}
	return closure
}

// closureComponents returns the subset of components that contain at least one
// seed node — the [][]string grouping RunPropagationScoped iterates to recompute
// only the dirty closure. dirtyComponentClosure flattens this for the unit-level
// closure invariant; the recompute loop needs the per-component grouping.
func closureComponents(seed map[string]bool, components [][]string) [][]string {
	var touchedComponents [][]string
	for _, component := range components {
		for _, id := range component {
			if seed[id] {
				touchedComponents = append(touchedComponents, component)
				break
			}
		}
	}
	return touchedComponents
}

// currentPropagatedAccessor builds the diffMetadataUpdates current-value accessor
// over the already-fetched nodeByID map: it reads the persisted propagated_*
// metadata via kgtypes.Value — no extra wire read. Returns nil when nodeByID is
// nil (the no-personality path) so diffMetadataUpdates treats it as the cold case
// and keeps every row, preserving the prior unconditional-write behavior there.
func currentPropagatedAccessor(nodeByID map[string]*knowledgev1.Node) func(id, key string) string {
	if nodeByID == nil {
		return nil
	}
	return func(id, key string) string {
		if n, ok := nodeByID[id]; ok {
			return kgtypes.Value(n, key)
		}
		return ""
	}
}

// RunPropagation executes a FULL propagation across all thoughts — every
// connected component is recomputed. Thin wrapper over RunPropagationScoped with
// dirtySeed=nil (the manual propagate tool and any forced/full pass want this).
// The changed-only writeback diff still applies, so a no-change full pass writes
// zero rows.
//
// The manual propagate tool (handlePropagateClient tools/thought.go) calls this
// full form; the warm-tick scoping rides RunPropagationScoped directly from
// runBackgroundPropagation. src forwards the resident corpus cache to the
// adjacency thought-arm when warm (the loop passes itself; an on-demand caller
// passes its CorpusProvider or nil).
func RunPropagation(ctx context.Context, gc Caller, profile *PersonalityProfile, nodeByID map[string]*knowledgev1.Node, src CorpusSource) (PropagationResult, error) {
	return RunPropagationScoped(ctx, gc, profile, nodeByID, nil, src)
}

// RunPropagationScoped executes propagation, optionally SCOPED to the
// connected-component closure of dirtySeed. Single adjacency call up front;
// chargeMap fetched ONCE (T3 perf lock); per-component matrix build uses the
// prebuilt adj subset and chargeMap subset.
//
// When dirtySeed is non-nil, only the components the closure touches are
// recomputed — every UNTOUCHED component is skipped and its persisted
// propagated_valence/propagated_magnitude (read from nodeByID via kgtypes.Value)
// carries forward EXACTLY. This is exact, not approximate: the trust matrix is
// block-diagonal per connected component (buildComponentMatrix), so an untouched
// component's converged DeGroot values are provably invariant between ticks. A
// quiet warm tick passes an EMPTY non-nil seed → recompute nothing. dirtySeed=nil
// ⇒ full pass (every component recomputed).
//
// Writeback rows (closure members only on a scoped pass) pass through
// diffMetadataUpdates with a current accessor reading propagated_* from nodeByID,
// so a single bulk_update_metadata writes only the rows whose value CHANGED —
// O(|changed|) regardless of N.
func RunPropagationScoped(ctx context.Context, gc Caller, profile *PersonalityProfile, nodeByID map[string]*knowledgev1.Node, dirtySeed map[string]bool, src CorpusSource) (PropagationResult, error) {
	nodeIDs, adj, err := fetchAdjacency(ctx, gc, "all", nil, src) // resident cache when warm; else drain.
	if err != nil {
		return PropagationResult{}, fmt.Errorf("thought: RunPropagationScoped: adjacency: %w", err)
	}
	if len(nodeIDs) == 0 {
		return PropagationResult{}, nil
	}
	chargeMap := chargeMapForThoughts(ctx, gc, nodeIDs)

	// One now for the whole pass: the read-time recency scalar (see the fold)
	// must be consistent across every component's matrix diagonal and init loop.
	now := time.Now()

	components := findConnectedComponents(nodeIDs, adj)
	result := PropagationResult{
		ThoughtsProcessed: len(nodeIDs),
		Components:        len(components),
		ValenceChanges:    make(map[string]float64),
		MagnitudeChanges:  make(map[string]float64),
	}

	// SCOPE: on a warm tick (non-nil seed) recompute ONLY the components the dirty
	// closure touches; untouched components carry forward their persisted
	// propagated_* unchanged. The gate is dirtySeed != nil, NOT len > 0: a quiet
	// warm tick passes an EMPTY non-nil seed and must recompute NOTHING (closure of
	// the empty set is empty), whereas a cold-start full pass passes nil and
	// recomputes every component.
	recomputed := components
	if dirtySeed != nil {
		recomputed = closureComponents(dirtySeed, components)
	}

	// Accumulate per-thought writeback rows; single bulk update at the
	// end (T2/T3 perf lock — no per-thought wire writes).
	var allUpdates []map[string]any
	// bandByNode records each recomputed member's per-component writeback band,
	// keyed by node id. Declared ONCE above the loop: a component's band is derived
	// from its own convergence residual, and every member of that component shares
	// it. (Declaring it inside the loop would retain only the last component's
	// entries — a silent trap the diff closure below depends on avoiding.)
	bandByNode := make(map[string]residualBand, len(nodeIDs))
	for _, component := range recomputed {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("propagation cancelled: %w", err)
		}
		matrix := buildComponentMatrix(component, adj, chargeMap, profile, nodeByID, now)

		// Hoist initial valence + local magnitude OUT of the per-thought
		// init loop — pure computation over the prebuilt chargeMap (T3
		// perf lock — no gc.Call in the per-id loop).
		initialValence := make(map[string]float64, len(component))
		localMagnitude := make(map[string]float64, len(component))
		for _, id := range component {
			props := computePropertiesFromCharges(chargeMap[id], now)
			initialValence[id] = props.Valence
			localMagnitude[id] = props.Magnitude
		}

		propagatedValence, vIter, vConverged, vResidual := PropagateValence(matrix, initialValence, defaultMaxIterations, defaultEpsilon)
		result.Iterations += vIter
		propagatedMagnitude, mIter, mConverged, mResidual := PropagateMagnitude(matrix, localMagnitude, defaultMaxIterations, defaultEpsilon)
		result.Iterations += mIter

		// Per-component convergence: a component converges only when BOTH its valence
		// and magnitude passes did. One slow clique no longer masks the converged
		// majority — each non-converged component is recorded with its size + residuals.
		if vConverged && mConverged {
			result.ComponentsConverged++
		} else {
			result.NonConverged = append(result.NonConverged, NonConvergedComponent{
				Size:              len(component),
				ValenceResidual:   vResidual,
				MagnitudeResidual: mResidual,
			})
		}

		// Per-component writeback band: floor at writebackDeadband, widen to the
		// component's oscillation amplitude. A converged component has residual ≈ 0,
		// so its band stays at the floor (converged behavior is unchanged). A
		// non-converged component oscillates within ~residual each tick; band =
		// k·residual with k=2 as the empirical safety margin (k=1 is the tighter
		// documented fallback — one residual width — if churn must be traded for
		// freshness). Below this band the value is jitter no consumer can observe, so
		// re-persisting it is pure churn.
		vBand := max(writebackDeadband, 2*vResidual)
		mBand := max(writebackDeadband, 2*mResidual)
		for _, id := range component {
			bandByNode[id] = residualBand{valence: vBand, magnitude: mBand}
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

	// Derive the backward-compat Converged flag (now "every recomputed component
	// converged") and bound the NonConverged list to the worst-K by residual,
	// summarizing the rest via NonConvergedOmitted.
	finalizeConvergence(&result)

	// Changed-only writeback: drop rows whose recomputed propagated_* is within the
	// per-component band of the persisted value (carry-forward equality), so the bulk
	// write is O(|changed|). The band is per-node (bandByNode) and per-key: a value
	// that drifted by less than its component's band is not re-persisted. nodeByID may
	// be nil (no-personality path) — diffMetadataUpdatesFunc then keeps every row (cold
	// case), preserving the prior full-write behavior.
	bulkPersistMetadata(ctx, gc, diffMetadataUpdatesFunc(allUpdates, currentPropagatedAccessor(nodeByID), bandedWritebackPredicate(bandByNode)))
	return result, nil
}

// bandedWritebackPredicate builds the propagated_* diff predicate: for each row it
// selects the node's per-component band (bandByNode) and the per-key band
// (propagated_valence → valence, propagated_magnitude → magnitude), then gates the
// write on propagatedValueChangedBand. A node absent from bandByNode falls back to
// the fixed floor — a totality-only branch, unreachable for propagation writeback
// rows since every recomputed member is recorded before the writeback.
func bandedWritebackPredicate(bandByNode map[string]residualBand) func(id, key, cur, want string) bool {
	return func(id, key, cur, want string) bool {
		band, ok := bandByNode[id]
		if !ok {
			return propagatedValueChangedBand(cur, want, writebackDeadband)
		}
		b := band.valence
		if key == "propagated_magnitude" {
			b = band.magnitude
		}
		return propagatedValueChangedBand(cur, want, b)
	}
}

// finalizeConvergence derives Converged (len(NonConverged)==0) and caps the
// NonConverged list to the worst nonConvergedReportCap components by max residual,
// recording how many were omitted. Each component's rank residual is the larger of
// its valence/magnitude residual.
func finalizeConvergence(result *PropagationResult) {
	worst := func(c NonConvergedComponent) float64 {
		if c.ValenceResidual > c.MagnitudeResidual {
			return c.ValenceResidual
		}
		return c.MagnitudeResidual
	}
	sort.Slice(result.NonConverged, func(i, j int) bool {
		return worst(result.NonConverged[i]) > worst(result.NonConverged[j])
	})
	if len(result.NonConverged) > nonConvergedReportCap {
		result.NonConvergedOmitted = len(result.NonConverged) - nonConvergedReportCap
		result.NonConverged = result.NonConverged[:nonConvergedReportCap]
	}
	// Derive AFTER the cap using the omitted count so a capped run with omitted
	// non-converged components is still correctly reported as not-all-converged.
	result.Converged = len(result.NonConverged) == 0 && result.NonConvergedOmitted == 0
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
	// (compileMutateBulkMetadata) and rides the Execute carrier seam. Use the
	// reflect-inert variant: this propagated_valence/propagated_magnitude writeback
	// is the reflection pass's OWN write and must NOT advance the reflect dirty-gen
	// (T1-1 self-trigger fix).
	if err := executeReflectInertMutate(ctx, gc, args); err != nil {
		slog.Warn("thought: bulkPersistMetadata: execute failed", "err", err)
	}
}

// diffMetadataUpdates filters a desired writeback slice down to ONLY the rows
// whose value actually changed versus what is already persisted — the O(|changed|)
// gate shared by both metadata writeback sites (persistClusterAssignments and
// bulkPersistMetadata). Each desired row has the bulk_update_metadata shape
// {"id": string, "metadata": map[string]string}; a row is kept when at least one
// of its metadata keys' desired value differs from current(id, key), and dropped
// when every key already equals the persisted value.
//
// current is a caller-supplied accessor closed over the already-fetched node map
// (via kgtypes.Value) — no extra wire read. A NIL current accessor is the COLD
// case (no persisted values to compare against, e.g. first pass): every row is
// kept. A row whose "metadata" is absent or not a map[string]string is passed
// through unchanged (cannot prove it unchanged, so never silently drop it).
//
// The exact-string "changed" predicate (cur != want) is the cluster_id semantics;
// the propagated_* site supplies a per-node banded predicate instead via
// diffMetadataUpdatesFunc.
func diffMetadataUpdates(desired []map[string]any, current func(id string, key string) string) []map[string]any {
	return diffMetadataUpdatesFunc(desired, current, func(_, _, cur, want string) bool { return cur != want })
}

// diffMetadataUpdatesFunc is diffMetadataUpdates parameterized by the per-key
// "did this value change enough to persist" predicate keyChanged(id, key, cur,
// want). It keeps a row when keyChanged reports true for at least one of the row's
// metadata keys, and drops a row when every key reports unchanged. The nil-current
// cold case, the id==""/non-map passthrough, and the any-key row semantics are
// identical to diffMetadataUpdates — only the per-key comparison is pluggable. The
// predicate receives id+key (not just cur/want) so the propagated_* writeback can
// select a PER-NODE, per-key band rather than a single global deadband.
func diffMetadataUpdatesFunc(desired []map[string]any, current func(id string, key string) string, keyChanged func(id, key, cur, want string) bool) []map[string]any {
	if current == nil {
		return desired // cold case: nothing persisted to diff against → keep all.
	}
	var changed []map[string]any
	for _, row := range desired {
		id, _ := row["id"].(string)
		meta, ok := row["metadata"].(map[string]string)
		if id == "" || !ok {
			changed = append(changed, row) // unprovable → keep.
			continue
		}
		rowChanged := false
		for k, v := range meta {
			if keyChanged(id, k, current(id, k), v) {
				rowChanged = true
				break
			}
		}
		if rowChanged {
			changed = append(changed, row)
		}
	}
	return changed
}
