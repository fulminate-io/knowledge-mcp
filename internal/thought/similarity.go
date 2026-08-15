// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// topicSimilarityMethod tags every relates-to edge the link pass writes between
// topic medoids. It marks the edge as similarity-origin (provenance) so it can be
// distinguished from hand-authored relates-to edges and cleaned up if ever needed.
const topicSimilarityMethod = "topic-similarity"

// DensifyParams carries the per-call overrides for the post-link within-topic kNN
// densification phase. A zero-value field resolves to its densify*Default
// const, identical to how a zero linkThreshold/mergeThreshold falls back to the HIGH
// package defaults — so a zero-value DensifyParams cleanly means "use all defaults".
type DensifyParams struct {
	Threshold  float64 // node-similarity gate; 0 → densifyNodeThresholdDefault
	K          int     // per-member kNN fan-out; 0 → densifyKDefault
	EdgeBudget int     // per-run total densify-edge cap; 0 → densifyEdgeBudgetDefault
}

// resolve returns a DensifyParams with every zero-value field replaced by its
// densify*Default const. Called once at the top of the densify phase so the rest of
// the primitive works with fully-resolved values.
func (d DensifyParams) resolve() DensifyParams {
	if d.Threshold <= 0 {
		d.Threshold = densifyNodeThresholdDefault
	}
	if d.K <= 0 {
		d.K = densifyKDefault
	}
	if d.EdgeBudget <= 0 {
		d.EdgeBudget = densifyEdgeBudgetDefault
	}
	return d
}

// Lever-time GROUP-similarity primitives shared by the merge cascade and the link
// pass. Both operate over 256-bit binary group embeddings using BitSimilarity, so
// the similarity measure is bit-agreement (no float cosine) and the cost is cheap
// in-memory popcount. These group-level primitives run only at the manual lever
// cadence (the merge cascade / link pass are lever-only); the hourly pass uses
// BitSimilarity directly for leaf attachment but does not invoke these helpers.

// groupEmbedding selects a topic's similarity operand: the topic-summary embedding
// when one exists (the durable, semantically-rich vector), else the bit-majority
// centroid over the member set. Empty/absent on both yields nil, and a nil
// embedding never clears any threshold (BitSimilarity returns 0 on a length
// mismatch), so a vector-less topic is inert rather than a false match.
func groupEmbedding(tp Topic) []byte {
	if len(tp.SummaryVector) == vectorBytes {
		return tp.SummaryVector
	}
	return tp.Centroid
}

// LinkCandidate is one surviving-topic pair classified at/above the link
// threshold by the link pass — a relates-to edge between the two topic medoids is
// materialized for it (next step). IndexA/IndexB index into the surviving-topics
// slice; MedoidA/MedoidB are the durable medoid anchors; Score is the bit
// similarity of the pair.
type LinkCandidate struct {
	IndexA, IndexB   int
	MedoidA, MedoidB string
	Score            float64
}

// pairwiseSimilarityMatrix builds the upper-triangular bit-similarity matrix over
// a slice of group embeddings: result[i][j] (i<j) is BitSimilarity(emb[i],emb[j]).
// The lower triangle and diagonal are left zero — callers read only i<j. Shared by
// the merge cascade (to seed its matrix) and the link pass.
func pairwiseSimilarityMatrix(embeddings [][]byte) [][]float64 {
	n := len(embeddings)
	m := make([][]float64, n)
	for i := range m {
		m[i] = make([]float64, n)
	}
	for i := range n {
		for j := i + 1; j < n; j++ {
			m[i][j] = BitSimilarity(embeddings[i], embeddings[j])
		}
	}
	return m
}

// MergeChain records one cascade union: the topic keys merged (From), the
// surviving topic key (To), and the bit similarity at which they merged. The chain
// reconstructs A+B→AB, AB+C→ABC for the lever's loud report.
type MergeChain struct {
	From []string
	To   string
	Sim  float64
}

// RunMergeCascade runs the agglomerative fixpoint merge in topic space: it
// repeatedly unions the closest topic pair at or above mergeThreshold, recomputes
// the union centroid over the FULL union member-vector set (so a union can newly
// clear the threshold against a third topic — the cascade), and re-evaluates until
// no pair clears. The topic count strictly decreases by one per merge, so the loop
// runs at most N-1 iterations and the fixpoint is guaranteed.
//
// Determinism: the argmax pair is selected by the total order (highest sim → lowest
// row index i → lowest column index j); union member sets are sorted; survivor and
// CreatedAt tie-breaks are deterministic — so identical inputs yield an identical
// merge chain and final partition (never Go map-iteration order). vectorIndex
// supplies the member thought vectors for the union centroid recompute; createdAt
// supplies the medoid/topic CreatedAt tie-break.
//
// Survivor identity: the union's MedoidID is the union member whose vector is
// bit-closest to the recomputed centroid (tie → smallest CreatedAt); the union's
// CreatedAt is the minimum over the merged topics; its SummaryContent/SummaryVector
// are cleared (the drift stage re-summarizes a merged topic).
func RunMergeCascade(
	topics []Topic,
	vectorIndex map[string][]byte,
	createdAt map[string]int64,
	mergeThreshold float64,
) (merged []Topic, chains []MergeChain) {
	// working is the mutable topic set; emb[i] is working[i]'s group embedding;
	// sim is the upper-triangular similarity matrix aligned to working.
	working := append([]Topic(nil), topics...)
	emb := make([][]byte, len(working))
	for i := range working {
		emb[i] = groupEmbedding(working[i])
	}
	sim := pairwiseSimilarityMatrix(emb)

	for {
		bestI, bestJ, bestSim, ok := argmaxMergePair(sim, len(working), mergeThreshold)
		if !ok {
			break // fixpoint — no pair clears the threshold
		}

		ti, tj := working[bestI], working[bestJ]
		union := unionTopics(ti, tj, vectorIndex, createdAt)
		chains = append(chains, MergeChain{
			From: []string{topicKey(ti), topicKey(tj)},
			To:   topicKey(union),
			Sim:  bestSim,
		})

		working, emb, sim = rebuildAfterMerge(working, emb, sim, bestI, bestJ, union)
	}

	return working, chains
}

// argmaxMergePair returns the upper-triangular pair (i<j) with the maximum
// similarity at or above mergeThreshold, with a deterministic (sim → lowest i →
// lowest j) tie-break: the scan visits pairs in ascending (i,j) order and only
// replaces on a STRICT similarity improvement, so the lowest-index pair wins any
// tie. ok is false when no pair clears the threshold.
func argmaxMergePair(sim [][]float64, n int, mergeThreshold float64) (bestI, bestJ int, bestSim float64, ok bool) {
	bestI, bestJ = -1, -1
	for i := range n {
		for j := i + 1; j < n; j++ {
			s := sim[i][j]
			if s < mergeThreshold {
				continue
			}
			if !ok || s > bestSim {
				ok = true
				bestSim, bestI, bestJ = s, i, j
			}
		}
	}
	return bestI, bestJ, bestSim, ok
}

// rebuildAfterMerge drops rows/cols i and j from the working set + similarity
// matrix, appends the merged topic, and recomputes ONLY the merged row's
// similarities — the surviving pairwise sims are carried over unchanged (an
// incremental update, not a full N² rebuild).
func rebuildAfterMerge(working []Topic, emb [][]byte, sim [][]float64, dropI, dropJ int, union Topic) ([]Topic, [][]byte, [][]float64) {
	newWorking := make([]Topic, 0, len(working)-1)
	newEmb := make([][]byte, 0, len(working)-1)
	oldIdx := make([]int, 0, len(working)-1) // newWorking[k] came from working[oldIdx[k]]
	for k := range working {
		if k == dropI || k == dropJ {
			continue
		}
		newWorking = append(newWorking, working[k])
		newEmb = append(newEmb, emb[k])
		oldIdx = append(oldIdx, k)
	}
	mergedPos := len(newWorking)
	newWorking = append(newWorking, union)
	newEmb = append(newEmb, groupEmbedding(union))

	newSim := make([][]float64, len(newWorking))
	for i := range newSim {
		newSim[i] = make([]float64, len(newWorking))
	}
	// Carry over surviving pairwise sims.
	for a := range oldIdx {
		for b := a + 1; b < len(oldIdx); b++ {
			lo, hi := oldIdx[a], oldIdx[b]
			if lo > hi {
				lo, hi = hi, lo
			}
			newSim[a][b] = sim[lo][hi]
		}
	}
	// Compute only the merged row (mergedPos) against every survivor.
	for a := range mergedPos {
		newSim[a][mergedPos] = BitSimilarity(newEmb[a], newEmb[mergedPos])
	}
	return newWorking, newEmb, newSim
}

// unionTopics merges two topics into one: the union of their member clusters and
// member thoughts (sorted+deduped for determinism), a centroid recomputed over the
// FULL union member-vector set, the survivor medoid bit-closest to that centroid
// (tie → smallest CreatedAt), and the minimum CreatedAt. Summary fields are cleared
// so the drift stage re-summarizes the merged topic.
func unionTopics(a, b Topic, vectorIndex map[string][]byte, createdAt map[string]int64) Topic {
	memberClusters := sortedUnion(a.MemberClusters, b.MemberClusters)
	memberThoughts := sortedUnion(a.MemberThoughtIDs, b.MemberThoughtIDs)

	memberVecs := make([][]byte, 0, len(memberThoughts))
	for _, id := range memberThoughts {
		if v, ok := vectorIndex[id]; ok {
			memberVecs = append(memberVecs, v)
		}
	}
	centroid := BitMajorityCentroid(memberVecs)

	primary := ""
	if len(memberClusters) > 0 {
		primary = memberClusters[0] // sorted → deterministic min label
	}
	createdAtMin := min(a.CreatedAt, b.CreatedAt)

	return Topic{
		PrimaryClusterID: primary,
		MemberClusters:   memberClusters,
		MemberThoughtIDs: memberThoughts,
		Centroid:         centroid,
		MedoidID:         medoidForUnion(memberThoughts, vectorIndex, createdAt, centroid),
		CreatedAt:        createdAtMin,
	}
}

// medoidForUnion returns the union member whose vector is bit-closest to the
// recomputed centroid, breaking ties by the smallest CreatedAt (then lowest ID for
// total determinism). Returns "" when no member has a vector.
func medoidForUnion(memberIDs []string, vectorIndex map[string][]byte, createdAt map[string]int64, centroid []byte) string {
	bestID := ""
	bestSim := -1.0
	var bestCreated int64
	for _, id := range memberIDs {
		v, ok := vectorIndex[id]
		if !ok {
			continue
		}
		s := BitSimilarity(v, centroid)
		switch {
		case bestID == "" || s > bestSim:
			bestID, bestSim, bestCreated = id, s, createdAt[id]
		case s == bestSim:
			// Tie on similarity → smallest CreatedAt, then lowest ID.
			c := createdAt[id]
			if c < bestCreated || (c == bestCreated && id < bestID) {
				bestID, bestCreated = id, c
			}
		}
	}
	return bestID
}

// topicKey is the stable chain identifier for a topic: its medoid (the durable
// anchor) when present, else its primary cluster label.
func topicKey(t Topic) string {
	if t.MedoidID != "" {
		return t.MedoidID
	}
	return t.PrimaryClusterID
}

// sortedUnion returns the sorted, de-duplicated union of two string slices.
func sortedUnion(a, b []string) []string {
	set := make(map[string]struct{}, len(a)+len(b))
	for _, s := range a {
		set[s] = struct{}{}
	}
	for _, s := range b {
		set[s] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// linkReport tallies the link-materialization pass for the lever's loud report.
type linkReport struct {
	Created       []LinkCandidate // newly written medoid relates-to edges
	AlreadyLinked int             // candidate pairs whose medoids already share an edge
}

// unorderedPairKey keys a medoid pair regardless of direction so an existing
// edge in either direction counts as already-linked.
func unorderedPairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

// MaterializeLinks writes a relates-to edge between the medoids of each link
// candidate that does not already share one, tagging every new edge with
// Method="topic-similarity" (provenance). It bulk-reads the existing relates-to
// edges among the candidate medoids ONCE up front (a single bulk read, not per-pair),
// skips pairs that are already linked (idempotent), and reports both the newly
// created links and the already-linked count. A candidate missing a medoid (no
// vector-backed identity) is skipped.
func MaterializeLinks(ctx context.Context, gc Caller, links []LinkCandidate) (linkReport, error) {
	var rep linkReport
	if len(links) == 0 {
		return rep, nil
	}

	// Collect the medoid set and bulk-read existing relates-to edges among them.
	medoidSet := map[string]struct{}{}
	for _, lc := range links {
		if lc.MedoidA != "" {
			medoidSet[lc.MedoidA] = struct{}{}
		}
		if lc.MedoidB != "" {
			medoidSet[lc.MedoidB] = struct{}{}
		}
	}
	medoids := make([]string, 0, len(medoidSet))
	for id := range medoidSet {
		medoids = append(medoids, id)
	}
	sort.Strings(medoids)

	existing := map[string]bool{}
	edges, err := fetchEdgesForNodeSet(ctx, gc, medoids, []kgtypes.EdgeType{kgtypes.EdgeRelatesTo})
	if err != nil {
		return rep, fmt.Errorf("thought: MaterializeLinks: read existing edges: %w", err)
	}
	for i := range edges {
		e := &edges[i]
		existing[unorderedPairKey(e.FromId, e.ToId)] = true
	}

	for _, lc := range links {
		if lc.MedoidA == "" || lc.MedoidB == "" || lc.MedoidA == lc.MedoidB {
			continue
		}
		key := unorderedPairKey(lc.MedoidA, lc.MedoidB)
		if existing[key] {
			rep.AlreadyLinked++
			continue
		}
		args, err := json.Marshal(map[string]any{
			"operation":    "link",
			"from":         lc.MedoidA,
			"to":           lc.MedoidB,
			"relationship": string(kgtypes.EdgeRelatesTo),
			"method":       topicSimilarityMethod,
		})
		if err != nil {
			return rep, fmt.Errorf("thought: MaterializeLinks: marshal link: %w", err)
		}
		if _, err := executeViaEngine(ctx, gc, "mutate", args); err != nil {
			return rep, fmt.Errorf("thought: MaterializeLinks: link write: %w", err)
		}
		existing[key] = true // dedup duplicate candidates within this batch
		rep.Created = append(rep.Created, lc)
	}

	return rep, nil
}

// RunGroupSimilarity classifies the SURVIVING topics (the merge cascade has
// already unioned the merge-band pairs, so the input is the post-merge set) at the
// LOWER link threshold: every pair whose group-embedding similarity is at or above
// linkThreshold is a link candidate (a relates-to edge between the two medoids is
// written by the link-materialization step). A pair below the threshold is neither
// linked nor merged. Because the cascade ran first, a merged-away topic is absent
// from topics and can never be a link candidate.
func RunGroupSimilarity(topics []Topic, linkThreshold float64) []LinkCandidate {
	embeddings := make([][]byte, len(topics))
	for i, tp := range topics {
		embeddings[i] = groupEmbedding(tp)
	}
	sim := pairwiseSimilarityMatrix(embeddings)

	var links []LinkCandidate
	for i := range topics {
		for j := i + 1; j < len(topics); j++ {
			if sim[i][j] >= linkThreshold {
				links = append(links, LinkCandidate{
					IndexA: i, IndexB: j,
					MedoidA: topics[i].MedoidID,
					MedoidB: topics[j].MedoidID,
					Score:   sim[i][j],
				})
			}
		}
	}
	return links
}

// simSurveyFloor is the lowest similarity the tuning survey counts. Pairs
// below it carry no threshold-tuning signal (nobody links at <0.70 on a
// 256-bit vector) and would swamp the histogram with the all-pairs noise floor.
const simSurveyFloor = 0.70

// simSurveyBucketWidth is the histogram resolution: 0.05-wide buckets from the
// floor up to 1.0 give six rows — coarse enough to read at a glance, fine
// enough to see where the mass sits relative to the link/merge thresholds.
const simSurveyBucketWidth = 0.05

// nearMissCap bounds the rendered below-threshold candidate list.
const nearMissCap = 15

// SimBucket is one histogram row of the pairwise group-similarity survey:
// the [Lo, Hi) similarity band and how many topic pairs fall in it.
type SimBucket struct {
	Lo, Hi float64
	Count  int
}

// SurveyGroupSimilarity surveys the pairwise group-similarity distribution for
// threshold tuning: a [0.70, 1.0] histogram in 0.05 buckets plus the top near-miss
// pairs sitting just BELOW the link threshold (capped). Pure analysis: writes nothing.
//
// Both outputs ride ONE pairwise loop: it computes s := BitSimilarity per pair
// (O(1) extra space, no second n×n float matrix beside RunGroupSimilarity's — at
// thousands of topics the matrix is the allocation that matters, not the popcounts)
// and feeds the histogram and the near-miss list from that same s.
func SurveyGroupSimilarity(topics []Topic, linkThreshold float64) (buckets []SimBucket, near []LinkCandidate) {
	embeddings := make([][]byte, len(topics))
	for i, tp := range topics {
		embeddings[i] = groupEmbedding(tp)
	}

	nBuckets := int(math.Round((1.0 - simSurveyFloor) / simSurveyBucketWidth))
	buckets = make([]SimBucket, nBuckets)
	for b := range buckets {
		buckets[b].Lo = simSurveyFloor + simSurveyBucketWidth*float64(b)
		buckets[b].Hi = buckets[b].Lo + simSurveyBucketWidth
	}

	for i := range topics {
		for j := i + 1; j < len(topics); j++ {
			s := BitSimilarity(embeddings[i], embeddings[j])
			if s < simSurveyFloor {
				continue
			}
			b := int((s - simSurveyFloor) / simSurveyBucketWidth)
			if b >= nBuckets {
				b = nBuckets - 1 // s == 1.0 lands in the top bucket
			}
			buckets[b].Count++
			if s < linkThreshold {
				near = append(near, LinkCandidate{
					IndexA: i, IndexB: j,
					MedoidA: topics[i].MedoidID,
					MedoidB: topics[j].MedoidID,
					Score:   s,
				})
			}
		}
	}

	// Highest-scoring near misses first — the pairs the next lower threshold
	// would catch — capped for the rendered report.
	sort.Slice(near, func(i, j int) bool {
		if near[i].Score != near[j].Score {
			return near[i].Score > near[j].Score
		}
		return near[i].MedoidA < near[j].MedoidA // deterministic tie-break
	})
	if len(near) > nearMissCap {
		near = near[:nearMissCap]
	}
	return buckets, near
}
