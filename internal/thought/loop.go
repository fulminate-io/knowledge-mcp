// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/graph"
)

// PropagationInterval is the period at which the client-side
// PropagationLoop fires runBackgroundPropagation. OQ1 lock: this is
// HOURLY, NOT 30 seconds. The charge-driven trigger is gone — every
// tick runs cluster detection + propagation unconditionally.
const PropagationInterval = time.Hour

// PropagationLoop is the client-side "subconscious" goroutine that
// periodically propagates charge influence through the thought graph
// and re-detects clusters. Constructed with NewPropagationLoop and
// owned by cmd/knowledge/mcp.go's runMCPMode (Phase 6 wiring).
//
// Carries a *graphclient.GraphClient directly per the T1 lock — no
// Store-shaped wrapper. Every read/write is a wire call through gc.
type PropagationLoop struct {
	gc *graphclient.GraphClient

	// interval is the per-tick cadence. Defaults to PropagationInterval
	// (one hour) in production. Tests override via newPropagationLoopForTest
	// to drive ticks deterministically without sleeping for an hour.
	interval time.Duration

	// onTick is the work performed on every ticker fire. Defaults to
	// p.runBackgroundPropagation in production. Tests inject a counter
	// closure so TestPropagationLoop_HourlyTick can assert tick semantics
	// without invoking real wire calls.
	onTick func()

	stopOnce sync.Once
	stopCh   chan struct{}
	inFlight sync.WaitGroup

	mu             sync.Mutex
	lastClusters   []ThoughtCluster
	lastProfile    *PersonalityProfile
	leidenState    *graph.LeidenState
	lastAdj        map[string][]string
	lastThoughtIDs []string
	lastTensions   []TensionReport
	lastBlindSpots []BlindSpotReport
}

// NewPropagationLoop creates a PropagationLoop backed by the given
// GraphClient. Single argument — no Store. wirePropagationRuntime in
// cmd/knowledge/mcp.go owns construction.
func NewPropagationLoop(gc *graphclient.GraphClient) *PropagationLoop {
	p := &PropagationLoop{
		gc:       gc,
		interval: PropagationInterval,
		stopCh:   make(chan struct{}),
	}
	p.onTick = p.runBackgroundPropagation
	return p
}

// GetClusters returns the most recently detected clusters and
// personality profile. Both may be nil if cluster detection has not
// run yet.
func (p *PropagationLoop) GetClusters() ([]ThoughtCluster, *PersonalityProfile) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastClusters, p.lastProfile
}

// GetTensionsAndBlindSpots returns the most recently computed tension
// and blind spot reports.
func (p *PropagationLoop) GetTensionsAndBlindSpots() ([]TensionReport, []BlindSpotReport) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastTensions, p.lastBlindSpots
}

// TriggerClusterDetection runs cluster detection synchronously and
// caches the result. Used by client-side reflective handlers when the
// cache is cold.
func (p *PropagationLoop) TriggerClusterDetection() {
	if p == nil {
		return
	}
	p.runClusterDetection()
}

// Start launches the background propagation goroutine. Call once after
// construction. Runs an initial cluster detection so cached
// tensions/blind spots are available immediately.
func (p *PropagationLoop) Start() {
	if p == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("PropagationLoop: panic recovered",
					"site", "PropagationLoop.Start",
					"err", r,
					"stack", string(debug.Stack()))
			}
		}()
		p.runClusterDetection()
		p.loop()
	}()
}

// Stop signals the loop to exit and waits up to deadline for the
// in-flight work to drain. Nil-safe (mirrors dream.Runner.Stop at
// cmd/knowledge/internal/dream/runner.go:335-338) — a nil receiver
// returns immediately. The stopOnce guard ensures repeated Stop()
// calls don't double-close the channel.
func (p *PropagationLoop) Stop(deadline time.Duration) {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})
	done := make(chan struct{})
	go func() {
		p.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		slog.Warn("PropagationLoop.Stop: deadline elapsed, abandoning in-flight work", "deadline", deadline)
	}
}

// loop runs as a background goroutine, ticking hourly and firing
// onTick on each tick. T3-1 fix: select{<-ticker.C; <-stopCh: return}
// pattern instead of `for range ticker.C` so Stop() can actually exit
// the loop body.
func (p *PropagationLoop) loop() {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.onTick()
		case <-p.stopCh:
			return
		}
	}
}

// runBackgroundPropagation fires cluster detection (every tick — no
// 5-minute guard per T3-1 lock) then runs propagation. inFlight.Add
// brackets the work so Stop() can wait for it.
func (p *PropagationLoop) runBackgroundPropagation() {
	if p == nil || p.gc == nil {
		return
	}
	p.inFlight.Add(1)
	defer p.inFlight.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()

	// Every tick triggers a cluster detection pass. No conditional
	// guard — the trigger semantics are deliberately simple per OQ1
	// lock (one hourly tick = one detection + one propagation).
	slog.Debug("thought: runBackgroundPropagation — triggering cluster detection")
	p.runClusterDetection()

	p.mu.Lock()
	profile := p.lastProfile
	p.mu.Unlock()

	// RunPropagation needs nodeByID for cluster_id resolution under
	// personality scalars. Skip the bulk hydrate when profile is nil
	// (no personality adjustment).
	result, err := RunPropagation(ctx, p.gc, profile, p.fetchNodeMap(ctx, profile))
	if err != nil {
		slog.Warn("background propagation failed", "error", err)
		return
	}
	if result.ThoughtsProcessed > 0 {
		slog.Info("propagation complete",
			"thoughts", result.ThoughtsProcessed,
			"components", result.Components,
			"iterations", result.Iterations,
			"converged", result.Converged,
			"duration", time.Since(start).Round(time.Millisecond))
	}
}

// fetchNodeMap pulls the full thought node map only when profile is
// non-nil (RunPropagation needs cluster_id for personality scalars).
// Skipping the hydrate in the nil-profile case avoids an unnecessary
// gc.Call on a no-personality path.
func (p *PropagationLoop) fetchNodeMap(ctx context.Context, profile *PersonalityProfile) map[string]*knowledgev1.Node {
	if profile == nil {
		return nil
	}
	ids, _ := listAllThoughtIDs(ctx, p.gc)
	return fetchNodesByIDs(ctx, p.gc, ids)
}

// runClusterDetection rebuilds clusters, personality, tensions, and
// blind spots. All reads via gc; persistence (cluster_id metadata)
// goes through mutate(bulk_update_metadata). T3-3 lock: the
// legacy server-side cluster-signal channel was deleted along with
// the charge-driven trigger; this body owns no further fan-out.
func (p *PropagationLoop) runClusterDetection() {
	if p == nil || p.gc == nil {
		return
	}
	p.inFlight.Add(1)
	defer p.inFlight.Done()

	start := time.Now()
	slog.Debug("thought: runClusterDetection starting")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1. Get current adjacency via the bulk wire call.
	nodeIDs, adj, err := fetchAdjacency(ctx, p.gc, "all", nil)
	if err != nil {
		slog.Warn("cluster detection: build adjacency failed", "error", err)
		return
	}
	gamma := 0.5

	// 2. Read previous Leiden state under lock.
	p.mu.Lock()
	prevLeidenState := p.leidenState
	prevThoughtIDs := p.lastThoughtIDs
	prevAdj := p.lastAdj
	p.mu.Unlock()

	// 3. Decide: full or incremental pass (no lock — local copies).
	newLeidenState, communityOf, _, isFull := runLeidenStep(prevLeidenState, prevThoughtIDs, prevAdj, nodeIDs, adj, gamma)

	// 4. Build clusters from partition.
	groups := make(map[string][]string)
	for _, id := range nodeIDs {
		groups[communityOf[id]] = append(groups[communityOf[id]], id)
	}
	clusters := buildClusterObjects(ctx, p.gc, groups)

	// 5. Compute personality profile.
	profile, err := ComputePersonalityScalars(ctx, p.gc, clusters, nil)
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
	blindSpots := ReflectBlindSpots(ctx, p.gc, clusters, adj)

	// 7. Store all results under lock.
	p.mu.Lock()
	p.leidenState = newLeidenState
	p.lastAdj = adj
	p.lastThoughtIDs = nodeIDs
	p.lastClusters = clusters
	p.lastProfile = &profile
	p.lastTensions = tensions
	p.lastBlindSpots = blindSpots
	p.mu.Unlock()

	slog.Info("thought: clusters detected",
		"count", len(clusters),
		"full_pass", isFull,
		"duration", time.Since(start))
}

// runLeidenStep decides between a full Leiden pass and an incremental
// update. Pure function — no DB, no locks, no mutation of inputs.
func runLeidenStep(prevState *graph.LeidenState, prevThoughtIDs []string, prevAdj map[string][]string, nodeIDs []string, adj map[string][]string, gamma float64) (newState *graph.LeidenState, communityOf map[string]string, edgeChanges []graph.EdgeChange, isFull bool) {
	isFull = prevState == nil || len(nodeIDs) != len(prevThoughtIDs)
	if isFull {
		slog.Debug("thought: runClusterDetection — full Leiden pass", "nodes", len(nodeIDs))
		newState = graph.NewLeidenState(nodeIDs, adj, gamma)
		communityOf = newState.CommunityOf
		return newState, communityOf, nil, true
	}
	edgeChanges = graph.ComputeEdgeChanges(prevAdj, adj)
	slog.Debug("thought: runClusterDetection — incremental pass", "nodes", len(nodeIDs), "edge_changes", len(edgeChanges))
	communityOf = prevState.UpdateIncremental(edgeChanges, adj)
	return prevState, communityOf, edgeChanges, false
}
