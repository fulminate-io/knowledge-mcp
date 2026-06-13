// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// densify.go is the post-link within-topic kNN densification primitive.
// After the medoid link pass, within each POST-CASCADE surviving topic it joins
// each member thought to its k most-similar co-members above a HIGH node-similarity
// threshold, writing provenance-stamped relates-to edges. The structural intent:
// the topic layer knows which thoughts belong together semantically; densification
// makes that structurally real so the next reflection pass's Leiden can fuse the
// within-topic components (kNN chains are dense enough to merge under CPM, unlike
// the single medoid bridge).
//
// The computation is pure and in-memory: it composes BitSimilarity (centroid.go)
// over the already-drained vector index — NO re-drain, NO LLM, NO wire calls in the
// selector itself. The single wire touch is ONE bulk idempotency pre-read (the
// caller supplies the existing-pair set) and ONE batched edge write (Phase 3).

// densifyCandidate is one selected undirected member pair (A < B canonically) with
// its bit-similarity score. It becomes a relates-to edge unless idempotency drops it.
type densifyCandidate struct {
	A, B  string
	Score float64
}

// selectTopicKNN is the pure per-topic kNN selector: for ONE topic, given its
// MemberThoughtIDs, the in-memory vectorIndex, the per-member fan-out k, and the
// node-similarity threshold, it returns the candidate undirected member-pairs.
//
// SHAPE: for each member m WITH a vector, BitSimilarity(vec[m], vec[other]) is
// computed against every OTHER co-topic member that has a vector. This is members²
// 256-bit popcounts — honestly cheap: the largest real topic is hundreds of
// members, so members² is at most ~10^5 popcounts per topic (sub-millisecond at the
// manual-only lever cadence). The COST BOUND THE TICKET CARES ABOUT IS EDGES
// WRITTEN (the per-run budget), NOT comparisons computed — these are different
// quantities and only the former is capped.
//
// Pairs with sim >= threshold are kept; m then takes its TOP-K by descending sim
// with a deterministic tie-break (higher sim first, then lexicographically smaller
// partner nodeID — NEVER Go map-iteration order, mirroring RunMergeCascade's
// determinism discipline). Each selected pair is canonicalized to an unordered
// (min,max) key and unioned across members into a per-topic candidate SET, so the
// kNN-from-each-endpoint overlap does not double-count (m picks n AND n picks m →
// ONE undirected edge). Members lacking a vector are skipped (defensive, mirroring
// BitMajorityCentroid's wrong-length skip).
func selectTopicKNN(memberIDs []string, vectorIndex map[string][]byte, k int, threshold float64) []densifyCandidate {
	// Stable member order for determinism (the input slice order is not guaranteed).
	members := append([]string(nil), memberIDs...)
	sort.Strings(members)

	// neighbor is one scored co-member candidate for a given source member.
	type neighbor struct {
		id    string
		score float64
	}

	seen := make(map[string]densifyCandidate) // canonical pair key → candidate (dedup)

	for _, m := range members {
		vm, ok := vectorIndex[m]
		if !ok {
			continue // vectorless member — skip (cannot score)
		}
		var cands []neighbor
		for _, other := range members {
			if other == m {
				continue
			}
			vo, ok := vectorIndex[other]
			if !ok {
				continue
			}
			s := BitSimilarity(vm, vo)
			if s >= threshold {
				cands = append(cands, neighbor{id: other, score: s})
			}
		}
		// Top-k by descending sim, tie-break by lexicographically smaller partner ID.
		sort.Slice(cands, func(i, j int) bool {
			if cands[i].score != cands[j].score {
				return cands[i].score > cands[j].score
			}
			return cands[i].id < cands[j].id
		})
		if len(cands) > k {
			cands = cands[:k]
		}
		for _, n := range cands {
			key := unorderedPairKey(m, n.id)
			if _, dup := seen[key]; dup {
				continue // the reciprocal pick already recorded this undirected pair
			}
			a, b := m, n.id
			if a > b {
				a, b = b, a
			}
			seen[key] = densifyCandidate{A: a, B: b, Score: n.score}
		}
	}

	// Deterministic output order: sorted by canonical (A,B).
	out := make([]densifyCandidate, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out
}

// fetchExistingPairs bulk-reads the EXISTING relates-to edges incident to the union
// of all member thought IDs in ONE round-trip (fetchEdgesForNodeSet — the same
// N+1-avoidance bulk RETURN_MODE_EDGES read the medoid link step and fetchAdjacency
// hold) and returns the set of canonical (min,max) member pairs already joined by a
// relates-to edge of ANY provenance. ONE bulk read for the ENTIRE densify pass (all
// topics), NOT per-topic and NOT per-pair.
//
// relates-to edges are written DIRECTIONAL (FromId→ToId) but are SEMANTICALLY
// undirected for dedup here, so both the existing edge keys and the candidate keys
// are canonicalized with unorderedPairKey — an existing B→A edge therefore blocks a
// candidate A-B regardless of direction. ANY provenance counts: authored edges,
// the topic-similarity medoid links, AND a prior densify run's topic-densify
// edges are all duplicates to be suppressed (the ticket's "never duplicate
// ANY-provenance edges").
//
// SAME-RUN VISIBILITY: densify runs AFTER the medoid-link pass within the SAME lever
// invocation. MaterializeLinks writes each medoid edge via a synchronous
// executeViaEngine(...,"mutate",...) Execute round-trip that returns only after the
// store commits, so a medoid edge written earlier this run is already committed and
// IS visible to this pre-read — a same-run medoid pair that is also a kNN candidate
// is therefore correctly suppressed (no read-before-write race).
func fetchExistingPairs(ctx context.Context, gc Caller, memberIDs []string) (map[string]bool, error) {
	existing := map[string]bool{}
	edges, err := fetchEdgesForNodeSet(ctx, gc, memberIDs, []kgtypes.EdgeType{kgtypes.EdgeRelatesTo})
	if err != nil {
		return nil, err
	}
	for i := range edges {
		e := &edges[i]
		existing[unorderedPairKey(e.FromId, e.ToId)] = true
	}
	return existing, nil
}

// dropExisting filters candidates whose canonical (min,max) key is already present
// in the existing-pair set (any provenance, either direction). Returns the survivors
// in the input order. A re-run over a topic whose kNN edges already exist therefore
// yields zero survivors (idempotent top-up).
func dropExisting(cands []densifyCandidate, existing map[string]bool) []densifyCandidate {
	out := cands[:0:0] // fresh backing array, preserve input order
	for _, c := range cands {
		if existing[unorderedPairKey(c.A, c.B)] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// densifyTopicStat is the per-touched-topic accounting the lever report renders: the
// stable topic key, the count of densify edges written for it, and the before/after
// structural component-count estimate.
type densifyTopicStat struct {
	TopicKey         string
	EdgesWritten     int
	BeforeComponents int
	AfterComponents  int
}

// densifyResult is the whole-pass densify output: the final budget-capped edge set
// (across all topics), the per-topic stats, and the budget-hit accounting.
type densifyResult struct {
	Edges     []densifyCandidate
	PerTopic  []densifyTopicStat
	BudgetHit bool
	// StarvedTopics is how many topics (in the deterministic order) were entirely or
	// partially skipped because the per-run edge budget was exhausted — the loud
	// budget-hit line names this count.
	StarvedTopics int
}

// densifyTopicOrder sorts topics into the DETERMINISTIC TOTAL ORDER the budget
// truncation relies on: by the survivor topic's reconciled stable identity (the
// post-cascade min-member cluster_id, i.e. PrimaryClusterID — NOT any pre-cascade
// label), tie-broken by MedoidID for a guaranteed total order. Lexicographically-late
// topics starve first when the budget hits, so a budget-truncated run is reproducible
// and the starvation order is explicit.
func densifyTopicOrder(topics []Topic) []Topic {
	out := append([]Topic(nil), topics...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].PrimaryClusterID != out[j].PrimaryClusterID {
			return out[i].PrimaryClusterID < out[j].PrimaryClusterID
		}
		return out[i].MedoidID < out[j].MedoidID
	})
	return out
}

// memberLocalAdj builds the undirected member-local adjacency map over the topic's
// members from a set of canonical (min,max) pair keys present in pairKeys — used for
// the connected-component estimate. Only edges between two in-topic members count.
func memberLocalAdj(memberIDs []string, pairs []densifyCandidate) map[string][]string {
	inTopic := make(map[string]bool, len(memberIDs))
	for _, id := range memberIDs {
		inTopic[id] = true
	}
	adj := make(map[string][]string, len(memberIDs))
	for _, p := range pairs {
		if !inTopic[p.A] || !inTopic[p.B] {
			continue
		}
		adj[p.A] = append(adj[p.A], p.B)
		adj[p.B] = append(adj[p.B], p.A)
	}
	return adj
}

// existingTopicPairs reconstructs the within-topic existing relates-to pairs (as
// densifyCandidate keys) from the whole-pass existing set, restricted to this topic's
// members — the BEFORE adjacency seed for the component estimate.
func existingTopicPairs(memberIDs []string, existing map[string]bool) []densifyCandidate {
	var out []densifyCandidate
	for i := range memberIDs {
		for j := i + 1; j < len(memberIDs); j++ {
			a, b := memberIDs[i], memberIDs[j]
			if a > b {
				a, b = b, a
			}
			if existing[unorderedPairKey(a, b)] {
				out = append(out, densifyCandidate{A: a, B: b})
			}
		}
	}
	return out
}

// computeDensifyEdges is the whole-pass densify driver. Over the POST-CASCADE
// surviving topics, in the deterministic survivor-keyed order, it selects each
// topic's kNN candidates, drops any-provenance existing edges (idempotency), and
// accumulates the survivors under the per-run edge BUDGET — when the running total
// reaches params.EdgeBudget it STOPS adding (setting BudgetHit and counting the
// remaining starved topics). For each topic that contributes at least one candidate
// or had existing within-topic edges, it records a before/after structural
// component-count estimate.
//
// COMPONENT ESTIMATE — HONESTY CAVEAT: beforeComponents/afterComponents count
// connected components over ONLY the within-topic relates-to subgraph this pass holds
// in memory — NOT the full multi-edge-type whole-graph adjacency that the next
// reflection's Leiden re-runs over, and STRUCTURAL components NOT Leiden CPM
// communities. It is a cheap in-pass LOWER-BOUND indicator of the fusion the next
// pass will realize, never a prediction of the final community count. (This caveat is
// repeated in the rendered report line.)
//
// existing is the whole-pass any-provenance existing-pair set from fetchExistingPairs
// (one bulk read, shared across all topics). The driver does NO wire I/O itself —
// pure in-memory composition over the drained vectorIndex.
func computeDensifyEdges(topics []Topic, vectorIndex map[string][]byte, existing map[string]bool, params DensifyParams) densifyResult {
	var res densifyResult
	ordered := densifyTopicOrder(topics)

	for _, tp := range ordered {
		cands := dropExisting(selectTopicKNN(tp.MemberThoughtIDs, vectorIndex, params.K, params.Threshold), existing)

		// Apply the remaining per-run budget to THIS topic's survivors. BudgetHit is
		// set ONLY when at least one candidate is actually dropped — a run whose total
		// candidates exactly equals the budget emits all of them with BudgetHit=false.
		// A topic that loses ANY of its candidates to the budget (including one whose
		// budget is already fully exhausted → zero of its candidates land) is counted
		// as STARVED, so the loud line reports how many topics the cap shortchanged.
		remaining := max(params.EdgeBudget-len(res.Edges), 0)
		topicEdges := cands
		if len(topicEdges) > remaining {
			topicEdges = topicEdges[:remaining]
			res.BudgetHit = true
			res.StarvedTopics++
		}

		// BEFORE adjacency = existing within-topic relates-to edges; AFTER = BEFORE
		// plus this topic's newly-emitted densify edges.
		beforePairs := existingTopicPairs(tp.MemberThoughtIDs, existing)
		beforeAdj := memberLocalAdj(tp.MemberThoughtIDs, beforePairs)
		afterAdj := memberLocalAdj(tp.MemberThoughtIDs, append(append([]densifyCandidate(nil), beforePairs...), topicEdges...))

		// Only record a stat for a topic that actually participates (has new edges or
		// pre-existing within-topic structure to report on).
		if len(topicEdges) > 0 || len(beforePairs) > 0 {
			res.PerTopic = append(res.PerTopic, densifyTopicStat{
				TopicKey:         topicKey(tp),
				EdgesWritten:     len(topicEdges),
				BeforeComponents: len(findConnectedComponents(tp.MemberThoughtIDs, beforeAdj)),
				AfterComponents:  len(findConnectedComponents(tp.MemberThoughtIDs, afterAdj)),
			})
		}

		res.Edges = append(res.Edges, topicEdges...)
		// If BudgetHit fired on this topic, the NEXT iteration's top-of-loop guard
		// accounts the starved remainder (topics after this one). When this was the
		// last topic, no later topic exists to starve and StarvedTopics stays 0.
	}

	return res
}

// writeDensifyEdges materializes the candidate pairs as relates-to edges in ONE
// batched mutate(create_batch) Execute — NOT a per-edge loop (the load-bearing 1-RPC
// bound). Every edge is stamped Type=relates-to, Method="topic-densify",
// Confidence=densifyEdgeConfidence so it is provenance-identifiable (and discountable
// the moment a consumer reads edge metadata). Both endpoints are existing thoughts,
// so the batch carries edges-only (from_id/to_id, no new node bodies). An empty edge
// set is a no-op (no Execute). Returns the count of edges written.
//
// TENSION EXCLUSION: although tensionEdgeTypes includes
// EdgeRelatesTo, a densify edge is NEVER tension-eligible — fetchTensionEdges
// pre-filters every machine-Method edge (isMachineTensionMethod, which keys on the
// "topic-densify" Method stamped below) out of the tension predicate. A densify
// edge is clustering / near-duplicate signal, NOT propositional disagreement, so a
// high-|Δvalence| co-topic pair joined ONLY by a densify edge does NOT surface in
// query(mode:tensions). The Method tag is exactly what the tension filter excludes on.
func writeDensifyEdges(ctx context.Context, gc Caller, pairs []densifyCandidate) (int, error) {
	if len(pairs) == 0 {
		return 0, nil
	}
	edges := make([]map[string]any, 0, len(pairs))
	for _, p := range pairs {
		edges = append(edges, map[string]any{
			"from_id":    p.A,
			"to_id":      p.B,
			"type":       string(kgtypes.EdgeRelatesTo),
			"method":     densifyMethod,
			"confidence": densifyEdgeConfidence,
		})
	}
	args, err := json.Marshal(map[string]any{
		"operation": "create_batch",
		"edges":     edges,
	})
	if err != nil {
		return 0, fmt.Errorf("thought: writeDensifyEdges: marshal create_batch: %w", err)
	}
	if _, err := executeViaEngine(ctx, gc, "mutate", args); err != nil {
		return 0, fmt.Errorf("thought: writeDensifyEdges: edge write: %w", err)
	}
	return len(pairs), nil
}

// runDensifyPhase materializes within-topic kNN relates-to edges over the surviving
// post-cascade topics. It does ONE bulk idempotency pre-read over the union
// of all member thoughts, computes the budget-capped edge set + component estimates
// in memory, writes every edge in ONE batched create_batch, and fills the report's
// densify fields. A nil vectorIndex (the nil-scanner total-degrade band) sets the loud
// DensifySkippedReason and writes nothing — never panics. Called as the penultimate
// stage of runTopicPipeline (similarity_lever.go), immediately before the tree-link
// clique phase (runTreeLinkPhase, tree_link_write.go); lives here beside the densify
// primitives.
func (p *PropagationLoop) runDensifyPhase(ctx context.Context, topics []Topic, vectorIndex map[string][]byte, densify DensifyParams, rep *SimilarityReport) {
	rep.DensifyBudget = densify.EdgeBudget
	if len(vectorIndex) == 0 {
		rep.DensifySkippedReason = "no member-vector index available (nil/empty scanner drain) — densification SKIPPED (no edges written)"
		return
	}

	// ONE bulk pre-read of existing relates-to edges over the union of all member
	// thoughts across every surviving topic (the any-provenance idempotency set).
	memberSet := map[string]struct{}{}
	for _, tp := range topics {
		for _, id := range tp.MemberThoughtIDs {
			memberSet[id] = struct{}{}
		}
	}
	allMembers := make([]string, 0, len(memberSet))
	for id := range memberSet {
		allMembers = append(allMembers, id)
	}
	existing, eerr := fetchExistingPairs(ctx, p.gc, allMembers)
	if eerr != nil {
		rep.addStageError("densify idempotency pre-read failed; treating as no existing edges", eerr)
		existing = map[string]bool{}
	}

	result := computeDensifyEdges(topics, vectorIndex, existing, densify)

	written, werr := writeDensifyEdges(ctx, p.gc, result.Edges)
	if werr != nil {
		rep.addStageError("densify edge write failed", werr)
	}

	for _, st := range result.PerTopic {
		rep.DensifyPerTopic = append(rep.DensifyPerTopic, TopicDensifyStat(st))
	}
	rep.DensifyEdgesTotal = written
	rep.DensifyBudgetHit = result.BudgetHit
	rep.DensifyStarved = result.StarvedTopics
}
