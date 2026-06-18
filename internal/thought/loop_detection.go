// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/graph"
)

// loop_detection.go holds the per-tick cluster-detection + Leiden-orchestration
// half of the PropagationLoop: the dirty-seed derivation, the detection pass
// (adjacency → Leiden full/incremental → post-Leiden leaf-attachment fallback →
// clusters → personality → tensions), the locked result store, the cold-start
// rehydration, and the Leiden step. The loop lifecycle (Start/Stop/loop,
// runBackgroundPropagation, accounting) lives in loop.go.

// deriveNodeDirtySeed drains the full thought-node payloads and derives the
// NODE-level dirty leg (thoughts whose UpdatedAt exceeds the persisted watermark)
// plus the max-UpdatedAt watermark over the FRESH per-tick browse — both NEVER
// over the closure/seed (the watermark must cover externally-changed untouched
// nodes too). Returns ok=false (already logged) when the browse fails. The
// EDGE-level leg is added by the caller from runLeidenStep's edgeChanges.
func (p *PropagationLoop) deriveNodeDirtySeed(ctx context.Context) (dirtySeed map[string]bool, maxWatermark int64, ok bool) {
	nodes, err := fetchAllThoughtNodes(ctx, p.gc)
	if err != nil {
		slog.Warn("cluster detection: thought-node browse failed", "error", err)
		return nil, 0, false
	}
	prevWatermark := readLastReflectedWatermark(ctx, p.gc)
	dirtySeed = make(map[string]bool)
	for _, n := range nodes {
		if n.UpdatedAt > maxWatermark {
			maxWatermark = n.UpdatedAt
		}
		if n.UpdatedAt > prevWatermark {
			dirtySeed[n.Id] = true // NODE-level dirty: thought changed since last pass.
		}
	}
	return dirtySeed, maxWatermark, true
}

// runClusterDetection rebuilds clusters, personality, tensions, and
// blind spots. All reads via gc; persistence (cluster_id metadata)
// goes through mutate(bulk_update_metadata). T3-3 lock: the
// legacy server-side cluster-signal channel was deleted along with
// the charge-driven trigger; this body owns no further fan-out.
//
// When a member-vector scanner is wired it ALSO drains the vector index at
// detection time and runs the post-Leiden leaf-attachment fallback
// (runLeafAttachment) before the groups build, so a single-edge singleton joins its
// neighbor's cluster at a per-provenance similarity gate; a nil scanner skips that
// step with a loud WARN and detection completes exactly as before.
func (p *PropagationLoop) runClusterDetection() {
	if p == nil || p.gc == nil {
		return
	}
	p.inFlight.Add(1)
	defer p.inFlight.Done()

	// Compute-stage cancellation gate (bind-first startup): skip the whole detection pass if a
	// daemon Stop (baseCancel) already began — bounds the post-cancel tail so a Stop
	// observed before this stage never starts a fresh multi-minute Leiden run.
	if err := p.baseContext().Err(); err != nil {
		return
	}

	start := time.Now()
	slog.Debug("thought: runClusterDetection starting")

	// Inner budget for the cluster-detection drain. Sized (5m) for the real ~6k
	// corpus: fetchAdjacency("all") here issues the same handful of bulk reads
	// (adjacency edges + one bulk EdgeKGContains session-membership read) + the
	// paged node browse as RunPropagationScoped — no per-thought traversal fan-out.
	// This ctx runs SYNCHRONOUSLY inside runBackgroundPropagation's 6m outer
	// bracket, so it is effectively capped by the outer remaining time — the outer
	// is set >= this inner (6m >= 5m).
	ctx, cancel := context.WithTimeout(p.baseContext(), 5*time.Minute)
	defer cancel()

	// 1. Get current adjacency via the bulk wire call.
	nodeIDs, adj, err := fetchAdjacency(ctx, p.gc, "all", nil)
	if err != nil {
		// LOUD degradation: distinguish a budget cap from a real failure so a
		// capped detection pass is never silently dropped. nodeIDs is the count
		// drained before the cap (may be partial on a mid-drain deadline).
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("thought: cluster detection budget exceeded — adjacency drain did not complete this tick; "+
				"clusters not refreshed (full-corpus paged browse exceeds the per-tick budget)",
				"thoughts_drained", len(nodeIDs),
				"budget", (5 * time.Minute).String(),
				"elapsed", time.Since(start).Round(time.Millisecond))
			return
		}
		slog.Warn("cluster detection: build adjacency failed", "error", err)
		return
	}
	gamma := 0.5

	// 1b. NODE-level dirty leg + the max-UpdatedAt watermark, both derived from the
	// FRESH per-tick thought-node browse (never the closure/seed).
	dirtySeed, maxWatermark, ok := p.deriveNodeDirtySeed(ctx)
	if !ok {
		return // browse failed — already logged.
	}

	// 2. Read previous Leiden state under lock. Consume forceFullNext: when the
	// backstop forced this tick, drop any in-memory state and SKIP the cold-start
	// rehydrate below so runLeidenStep takes the TRUE full branch (a full
	// graph.NewLeidenState recompute) rather than rehydrating the prior partition —
	// resetting accumulated DF-Leiden incremental drift.
	prevLeidenState, prevAdj, forceFull := p.readLeidenBaseline()

	// 2b. Cold start: rehydrate the Leiden partition from the persisted cluster_id
	// metadata instead of forcing a full pass after every daemon restart. SKIPPED on
	// a forced backstop tick — there the nil prevLeidenState must reach runLeidenStep
	// as nil so it takes the full branch (rehydrate would rebuild a partition and
	// defeat the forced full recompute).
	if prevLeidenState == nil && !forceFull {
		prevLeidenState, prevAdj = p.rehydrateColdStart(ctx, adj, gamma)
	}

	// 3. Decide: full (no usable prior state) or incremental pass (no lock — local copies).
	newLeidenState, communityOf, edgeChanges, isFull := runLeidenStep(prevLeidenState, prevAdj, nodeIDs, adj, gamma)

	// 3b. EDGE-level dirty leg: add both endpoints of every edge change to the seed.
	// This leg is LOAD-BEARING for the bridging-edge JOIN: compositeDB.Link
	// (composite_db_write.go:286) and AddEdge do NOT bump endpoint nodes' UpdatedAt,
	// so a bare bridging edge between two otherwise-unchanged thoughts is invisible
	// to the NODE-level leg (1b) and surfaces ONLY here, via the set-diff. On a
	// cold-start full pass edgeChanges is nil and the seed is unused (scoping off).
	for _, ec := range edgeChanges {
		dirtySeed[ec.From] = true
		dirtySeed[ec.To] = true
	}

	// 3c. Post-Leiden leaf-attachment fallback: a singleton thought with >=1
	// adjacency edge to a clustered thought joins the best centroid-similar reachable
	// cluster at a per-provenance gate. Mutates communityOf + newLeidenState.CommSize
	// in place HERE — before the groups build below — so the attached cluster_id is
	// persisted by the existing buildClusterObjects path AND the next tick's
	// incremental baseline (newLeidenState) stays consistent with the partition.
	p.runLeafAttachment(ctx, communityOf, newLeidenState.CommSize, adj)

	// 4. Build clusters from partition.
	groups := make(map[string][]string)
	for _, id := range nodeIDs {
		groups[communityOf[id]] = append(groups[communityOf[id]], id)
	}
	clusters := buildClusterObjects(ctx, p.gc, groups)

	// 5. Compute personality profile. Feed the charge→evidence adjacency so the
	// cross-cluster attribution leg participates (not nil — that left trust at 1.000).
	profile, err := ComputePersonalityScalars(ctx, p.gc, clusters, BuildEvidenceAdj(ctx, p.gc, clusters))
	if err != nil {
		slog.Warn("personality scalar computation failed", "error", err)
		return
	}

	// 6. Compute tensions and blind spots.
	tensions, err := ReflectTensions(ctx, p.gc)
	if err != nil {
		slog.Warn("tension computation failed", "error", err)
		tensions = nil
	}
	blindSpots := p.computeBlindSpots(ctx, nodeIDs, clusters)

	// 7. Store all results (incl. the dirty seed + watermark) under lock.
	p.storeDetectionResults(detectionResults{
		state: newLeidenState, adj: adj, clusters: clusters, profile: &profile,
		tensions: tensions, blindSpots: blindSpots, watermark: maxWatermark,
		dirtySeed: dirtySeed, isFull: isFull,
	})

	slog.Info("thought: clusters detected",
		"count", len(clusters),
		"full_pass", isFull,
		"duration", time.Since(start))
}

// computeBlindSpots builds the faceted epistemic-risk report for the on-demand
// blind_spots surface to serve from cache. It does the bulk-read shape the
// per-thought facets need — ONE influence pass + one bulk session-label read + one
// bulk charges read + one bulk node hydrate over nodeIDs — plus ONE bulk topic-doc
// browse (TopicGroupingByClusterID) for the topic rollup unit of the cluster-level
// belief-reversal view. The pooled cluster reversal REUSES the same charges map (no
// extra per-thought fetch); clusters come from the tick's Leiden partition. No
// per-thought fan-out, no N+1 — every read is a single bulk call.
func (p *PropagationLoop) computeBlindSpots(ctx context.Context, nodeIDs []string, clusters []ThoughtCluster) BlindSpotReport {
	influence := BlindSpotInfluenceVector(ctx, p.gc, nodeIDs)
	sessionByThought := FetchSessionLabelsByThought(ctx, p.gc, nodeIDs)
	charges := fetchChargesFor(ctx, p.gc, nodeIDs)
	nodeByID := fetchNodesByIDs(ctx, p.gc, nodeIDs)
	// Topic rollup for the cluster-level reversal unit: one bulk topic-doc browse,
	// loop-safe (in-package, Caller-based, no handler deps). Clusters sharing a
	// topic-summary doc roll into one unit; topicless clusters stay raw.
	topics := TopicGroupingByClusterID(ctx, p.gc)
	return classifyBlindSpots(blindSpotInputs{
		thoughtIDs:       nodeIDs,
		charges:          charges,
		influence:        influence,
		nodeByID:         nodeByID,
		sessionByThought: sessionByThought,
		clusters:         clusters,
		topics:           topics,
		now:              p.clockNow(),
	})
}

// runLeafAttachment drains the member-vector index at detection time (GATED on a
// wired scanner, TIMED, LOUD-SKIP when absent) and runs the post-Leiden
// leaf-attachment pass, mutating communityOf + commSize in place. The drain is the
// one cost the leaf-attachment fallback adds to the hourly pass: it is paid ONLY when a scanner is wired
// (production), skipped with a loud WARN otherwise (tests / degraded clients run
// exactly today's behavior — detection completes normally, no panic).
//
// commSize is newLeidenState.CommSize (community → member count); attachLeaves
// zeros an attached leaf's old singleton entry and grows the target so both maps
// stay consistent for persistence and the next incremental baseline.
func (p *PropagationLoop) runLeafAttachment(ctx context.Context, communityOf map[string]string, commSize map[string]int, adj map[string][]string) {
	if p.scanner == nil {
		slog.Warn("thought: leaf-attachment SKIPPED — no member-vector scanner wired; " +
			"singletons not attached this pass (degraded mode: detection completes normally)")
		return
	}
	drainStart := time.Now()
	vectorIndex, err := drainVectorIndex(ctx, p.scanner)
	if err != nil {
		slog.Warn("thought: leaf-attachment SKIPPED — member-vector drain failed; singletons not attached this pass",
			"error", err,
			"drain_ms", time.Since(drainStart).Milliseconds())
		return
	}
	drainMS := time.Since(drainStart).Milliseconds()

	// Candidate leaves (singletons) for the ONE bulk provenance edge read.
	var leafIDs []string
	for id, comm := range communityOf {
		if comm == id && commSize[id] == 1 {
			leafIDs = append(leafIDs, id)
		}
	}
	leafProvenance := buildLeafProvenance(ctx, p.gc, leafIDs)

	stats := attachLeaves(communityOf, commSize, adj, vectorIndex, leafProvenance)

	slog.Info("thought: leaf-attachment",
		"drain_ms", drainMS,
		"candidates", stats.candidates,
		"attached", stats.attached,
		"gate_vetoed", stats.gateVetoed,
		"vectorless_skipped", stats.vectorlessSkipped,
		"by_provenance", stats.byProvenance)
}

// readLeidenBaseline reads the prior Leiden state + adjacency under the lock and
// consumes the forceFullNext flag (clearing it). On a forced tick it returns a nil
// baseline so the caller skips rehydrate and runLeidenStep takes the full branch.
func (p *PropagationLoop) readLeidenBaseline() (prevState *graph.LeidenState, prevAdj map[string][]string, forceFull bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	prevState = p.leidenState
	prevAdj = p.lastAdj
	forceFull = p.forceFullNext
	p.forceFullNext = false
	if forceFull {
		prevState = nil
		prevAdj = nil
	}
	return prevState, prevAdj, forceFull
}

// detectionResults bundles one runClusterDetection tick's outputs for the
// single locked store. isFull gates whether the dirty seed is published (a
// cold-start full pass leaves it nil → RunPropagationScoped recomputes every
// component, full pass preserved).
type detectionResults struct {
	state      *graph.LeidenState
	adj        map[string][]string
	clusters   []ThoughtCluster
	profile    *PersonalityProfile
	tensions   []TensionReport
	blindSpots BlindSpotReport
	watermark  int64
	dirtySeed  map[string]bool
	isFull     bool
}

// storeDetectionResults publishes one tick's detection outputs under p.mu. On a
// cold-start full pass the seed is unused (scoping applies only to warm
// incremental ticks), so lastDirtySeed is left nil.
func (p *PropagationLoop) storeDetectionResults(r detectionResults) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.leidenState = r.state
	p.lastAdj = r.adj
	p.lastClusters = r.clusters
	p.lastProfile = r.profile
	p.lastTensions = r.tensions
	p.lastBlindSpots = r.blindSpots
	// A tick that reaches storeDetectionResults has successfully computed
	// clusters+profile+tensions+blindSpots; the early-return failure paths
	// (loop_detection.go adjacency/browse/personality errors) never reach this call,
	// so lastComputed stays false on a failed/never-run tick — the cold sentinel the
	// cache-serve handlers read.
	p.lastComputed = true
	p.lastWatermark = r.watermark
	if r.isFull {
		p.lastDirtySeed = nil
	} else {
		p.lastDirtySeed = r.dirtySeed
	}
}

// rehydrateColdStart reconstructs the Leiden partition from the persisted
// cluster_id metadata the loop itself writes every tick (partitionFromPersisted),
// so a daemon restart does NOT force a full pass. Called only when there is no
// in-memory state. Returns the rehydrated state and the baseline adjacency to use
// for the next tick's set-diff; on an empty partition (true first run) or a read
// failure it returns (nil, the supplied adj) so runLeidenStep takes the full pass.
func (p *PropagationLoop) rehydrateColdStart(ctx context.Context, adj map[string][]string, gamma float64) (*graph.LeidenState, map[string][]string) {
	partition, err := partitionFromPersisted(ctx, p.gc)
	if err != nil {
		slog.Warn("thought: cold-start partition read failed — falling back to full pass", "error", err)
		return nil, adj
	}
	if len(partition) == 0 {
		return nil, adj // true first run — nothing to rehydrate.
	}
	state := graph.RehydrateLeidenState(partition, adj, gamma)

	// Treat the CURRENT adjacency as the settled rehydrate baseline so the next
	// tick's set-diff (ComputeEdgeChanges) yields only edges genuinely new since
	// this rehydrate.
	//
	// ACCEPTED LIMITATION: because the baseline is sampled at rehydrate time, a new
	// edge created between two ALREADY-CLUSTERED thoughts while the daemon was down
	// is folded into this baseline and is therefore NOT caught by the incremental
	// path — its endpoints are already in the partition, so neither seedNewNodes nor
	// the set-diff surfaces it, and the JOIN it might have triggered is missed until
	// that region is next touched (a new incident edge re-seeds it). This drift is
	// deliberately accepted for v1 and is bounded by the scheduled full-pass
	// backstop (the periodic full recompute owns closing it).
	baselineAdj := adj

	rehydratedComms := make(map[string]struct{}, len(partition))
	for _, c := range partition {
		rehydratedComms[c] = struct{}{}
	}
	slog.Info("thought: rehydrated Leiden state from persisted cluster_id",
		"communities", len(rehydratedComms), "nodes", len(partition))
	return state, baselineAdj
}

// seedNewNodes pre-seeds thoughts that are absent from the partition so the
// Dynamic Frontier incremental path actually processes them. For every id in
// nodeIDs not already in state.CommunityOf it adds the node as its own singleton
// community (CommunityOf[id]=id, CommSize[id]=1 — mirroring the single-node
// initialization the full pass does) and collects every edge incident to that new
// node as a graph.EdgeChange{Removed:false}. Nodes already present are untouched.
// Returns the incident-edge seeds for UpdateIncremental.
//
// WHY: UpdateIncremental marks an endpoint affected ONLY if it already exists in
// CommunityOf (leiden_incremental.go), so a genuinely-new thought (no cluster_id
// → absent from the rehydrated/in-memory partition) would have its incident edges
// silently skipped and never cluster or bridge-JOIN. Pre-seeding puts it in
// CommunityOf so its incident edges become valid affected-seeds. This keeps
// leiden_incremental.go unchanged — all new-node handling is orchestration.
//
// The returned edges are NOT canonically deduplicated: UpdateIncremental builds
// its affected set as a map[string]bool keyed by endpoint, so a duplicate or
// reversed (From,To) seed is idempotent — listing each incident edge once per new
// node is sufficient.
func seedNewNodes(state *graph.LeidenState, nodeIDs []string, adj map[string][]string) []graph.EdgeChange {
	var seeds []graph.EdgeChange
	for _, id := range nodeIDs {
		if _, ok := state.CommunityOf[id]; ok {
			continue // already partitioned — leave it.
		}
		state.CommunityOf[id] = id // singleton community.
		state.CommSize[id] = 1
		for _, nb := range adj[id] {
			seeds = append(seeds, graph.EdgeChange{From: id, To: nb, Removed: false})
		}
	}
	return seeds
}

// runLeidenStep decides between a full Leiden pass and an incremental update.
// A full pass runs ONLY when there is no usable prior state (prevState == nil) —
// i.e. a true first run with no in-memory state and nothing to rehydrate from the
// persisted cluster_id metadata (the caller does the cold-start rehydrate before
// reaching here). Otherwise the step takes the incremental Dynamic Frontier path:
// it singleton-seeds any genuinely-new thought (absent from the partition) via
// seedNewNodes and feeds those seeds plus the prev→current adjacency set-diff to
// UpdateIncremental — so a new thought routes incrementally instead of forcing a
// full recompute on every new node.
//
// No DB, no locks. NOT pure on the incremental path: seedNewNodes and
// UpdateIncremental mutate prevState's partition maps in place (prevState is the
// returned newState in that case).
//
// edgeChanges is the warm-path set-diff frontier (the prev→current adjacency diff
// plus the new-node incident-edge seeds); it is returned so the caller can derive
// the dirty seed from the edge-change endpoints. It is nil on the full-pass branch
// (cold start), where scoping does not apply and the seed is unused.
func runLeidenStep(prevState *graph.LeidenState, prevAdj map[string][]string, nodeIDs []string, adj map[string][]string, gamma float64) (newState *graph.LeidenState, communityOf map[string]string, edgeChanges []graph.EdgeChange, isFull bool) {
	isFull = prevState == nil
	if isFull {
		slog.Debug("thought: runClusterDetection — full Leiden pass", "nodes", len(nodeIDs))
		newState = graph.NewLeidenState(nodeIDs, adj, gamma)
		communityOf = newState.CommunityOf
		return newState, communityOf, nil, true
	}
	// DIRTY-GEN SWAP POINT: this set-diff + new-node seed is the SOLE source of
	// UpdateIncremental's frontier. When the server-side dirty-gen changed-set axis
	// lands, only this derivation swaps to consume the server-supplied delta; the
	// UpdateIncremental call shape below is unchanged.
	newEdges := seedNewNodes(prevState, nodeIDs, adj)
	edgeChanges = append(graph.ComputeEdgeChanges(prevAdj, adj), newEdges...)
	slog.Debug("thought: runClusterDetection — incremental pass", "nodes", len(nodeIDs), "edge_changes", len(edgeChanges))
	communityOf = prevState.UpdateIncremental(edgeChanges, adj)
	return prevState, communityOf, edgeChanges, false
}
