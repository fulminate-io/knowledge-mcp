// SPDX-License-Identifier: Apache-2.0

package thought

// similarity_lever.go holds the MANUAL-ONLY operator lever worker
// (RunSimilarityPass) — the body that drives the entire topic layer: vector
// drain → per-cluster centroids → topic-doc reconciliation → agglomerative
// fixpoint merge cascade → topic-summary create → drift re-summary → medoid link
// pass. The per-account reflection single-flight guard is claimed by the async
// wrapper (StartSimilarityPass, similarity_async.go) BEFORE the goroutine runs
// this body, so a lever invocation still COALESCES with an in-flight reflection
// tick rather than stampeding a second concurrent recompute. NOTHING here runs on
// the hourly tick (the v1 lever-only decision) — the hourly pass is untouched.

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The HIGH-conservative default thresholds (link < merge). Lowered incrementally
// during shake-out. A topic pair at/above the link threshold gets a medoid
// relates-to edge; at/above the (higher) merge threshold it is unioned by the
// cascade.
const (
	similarityLinkThresholdDefault  = 0.90
	similarityMergeThresholdDefault = 0.97
)

// Densification defaults for the post-link within-topic kNN phase. The
// node-similarity threshold is a STRICTER gate than the topic-link threshold —
// densify joins two MEMBER THOUGHTS that are near-duplicates inside an already-
// coherent topic, so it sits ABOVE the (topic-medoid) link threshold's band. k
// bounds EDGES PER NODE (the per-member kNN fan-out); the budget bounds EDGES
// WRITTEN PER RUN across all topics (NOT comparisons computed). All three are
// per-call overridable; the conservative defaults mirror the link/merge shake-out
// story (start HIGH and bounded, lower/raise incrementally on the real corpus).
const (
	densifyNodeThresholdDefault = 0.93 // > similarityLinkThresholdDefault: a stricter near-duplicate gate
	densifyKDefault             = 2    // per-member kNN fan-out (ticket: "e.g. 2-3, configurable")
	densifyEdgeBudgetDefault    = 2000 // per-run cap on TOTAL densify edges written, loud on hit
)

// densifyMethod tags every relates-to edge the densification phase writes,
// distinguishing it from authored relates-to edges AND from the medoid
// "topic-similarity" links (topicSimilarityMethod) so densify edges are Method-keyed
// for cleanup or tension exclusion.
const densifyMethod = "topic-densify"

// densifyEdgeConfidence is the LOW explicit Confidence stamped on every densify edge
// to MARK its machine origin (below the authored-edge convention: a bare authored
// mutate(link) leaves Confidence 0, explicit-authored linker edges use higher values).
// CONFIDENCE-CONSUMER CENSUS (verified at source): NO current consumer reads edge
// Confidence/Method for thought-graph reflection/trust/clustering — fillSparseRows
// hardcodes adjacency weight 1.0, BuildTrustMatrix feeds on the unweighted adjacency
// map, and RunLeiden/CPM counts integer unweighted edges. So discounting machine edges
// is NOTED AS AVAILABLE (present + discountable once a consumer reads it) but NOT WIRED
// NOW — exactly the ticket's ask.
const densifyEdgeConfidence = 0.25

// SimilarityReport (the loud lever result) + addStageError + the report-row structs
// (TombstonedDoc, TopicDensifyStat) live in similarity_report.go — split out to keep
// this file under the 500-line cap.

// RunSimilarityPass is the pure worker body of the manual operator lever: it
// drives the full topic layer to completion. The reflection single-flight guard
// is NOT claimed here — the async wrapper (StartSimilarityPass) acquires it
// BEFORE spawning the goroutine that calls this, so the body must not re-acquire
// (that would self-deadlock). Zero-value thresholds fall back to the HIGH
// package-const defaults.
//
// Degradation has two distinct bands: (a) a nil scanner means no vector drain →
// no centroids → no topic work at all (a loud total degrade); (b) a live scanner
// with a nil summarizer still computes centroids, runs the merge cascade, and
// WRITES link edges (centroid-fallback group embedding) — only the summary create /
// drift re-summary bands are skipped, with a loud note.
func (p *PropagationLoop) RunSimilarityPass(ctx context.Context, linkThreshold, mergeThreshold float64, densify DensifyParams) (SimilarityReport, error) {
	if p == nil || p.gc == nil {
		return SimilarityReport{}, errors.New("similarity: propagation loop not running in this process")
	}

	// Stop()-drain bracket — a manual lever invocation is in-flight work.
	p.inFlight.Add(1)
	defer p.inFlight.Done()

	// Resolve thresholds: zero-value → HIGH defaults (link < merge).
	if linkThreshold <= 0 {
		linkThreshold = similarityLinkThresholdDefault
	}
	if mergeThreshold <= 0 {
		mergeThreshold = similarityMergeThresholdDefault
	}
	// Resolve densify overrides (zero-value fields → densify*Default consts), so the
	// post-link densification phase works with fully-resolved values.
	densify = densify.resolve()
	rep := SimilarityReport{LinkThreshold: linkThreshold, MergeThreshold: mergeThreshold}

	slog.Info("thought: similarity lever — manual pass requested",
		"link_threshold", linkThreshold, "merge_threshold", mergeThreshold,
		"densify_threshold", densify.Threshold, "densify_k", densify.K, "densify_edge_budget", densify.EdgeBudget)

	// (1) Current clusters — use the cached detection state when warm, else run a
	// detection pass first (the lever does not depend on the hourly cadence).
	clusters, _ := p.GetClusters()
	if len(clusters) == 0 {
		p.runClusterDetection()
		clusters, _ = p.GetClusters()
	}

	// (2) Drain member vectors via the injected scanner. A nil scanner → total
	// degrade (no centroids → no topic work), loud and non-panicking.
	vectorIndex, err := drainVectorIndex(ctx, p.scanner)
	if err != nil {
		rep.Degraded = true
		rep.DegradedNote = "no member-vector scanner wired — skipped ALL topic work (centroids, cascade, links, summaries): " + err.Error()
		// Densification is part of the skipped topic work — name it loudly too so the
		// report's densify section renders the skip rather than a silent zero.
		rep.DensifySkippedReason = "no member-vector scanner wired (no drain) — densification SKIPPED (no edges written)"
		slog.Warn("thought: similarity lever degraded — no scanner", "err", err)
		return rep, nil
	}

	// (3) Per-cluster centroids + medoids (lever-computed, never cached).
	ComputeClusterCentroids(clusters, vectorIndex)

	// (4–9) The topic lifecycle: build the topic set, run the merge cascade,
	// reconcile docs, create + drift-refresh summaries, and write link edges. Fills
	// the report's per-stage counts in place.
	p.runTopicPipeline(ctx, clusters, vectorIndex, linkThreshold, mergeThreshold, densify, &rep)

	// Partial-degrade note: a live scanner but a missing summarizer ran centroids +
	// cascade + links, skipping only summaries/drift. Loud and explicit. (Drift no
	// longer needs an embedder — it compares the live centroid against the stored
	// topic_centroid anchor — so the summarizer is the sole LLM-band gate.)
	if p.summarizer == nil {
		rep.Degraded = true
		rep.DegradedNote = "no summarizer wired — centroids, merge cascade, and link edges were computed/written, but topic summaries and drift re-summary were SKIPPED"
	}

	return rep, nil
}

// runTopicPipeline runs the topic lifecycle stages (build → cascade → reconcile →
// create → drift → link) over the centroid-populated clusters, filling rep's
// per-stage counts. Split out of RunSimilarityPass to keep each function focused;
// every stage degrades loudly (a failed read/write is logged, never fatal).
func (p *PropagationLoop) runTopicPipeline(
	ctx context.Context,
	clusters []ThoughtCluster,
	vectorIndex map[string][]byte,
	linkThreshold, mergeThreshold float64,
	densify DensifyParams,
	rep *SimilarityReport,
) {
	// CreatedAt per member (for the cascade's medoid/topic tie-break) + member
	// labels for the summary prompt content. ONE bulk hydrate over all members.
	createdAt, contentByCluster := p.memberCreatedAtAndContent(ctx, clusters)

	// Existing topic docs + the live partition, both read once and threaded through
	// reconciliation / create / drift. The document-type browse returns EVERY
	// `document` node (topic docs AND regular hand-written docs), so it is filtered
	// to topic-layer-created docs (isTopicDoc: medoid_id or topic_centroid present)
	// before any topic stage sees it. A regular document is never a topic-machinery
	// candidate — selecting by marker PRESENCE here is the data-loss guard.
	rawDocs, derr := drainThoughtBrowse(ctx, p.gc, string(kgtypes.NodeDocument), browsePageSize)
	if derr != nil {
		rep.addStageError("topic-doc read failed; treating as no existing docs", derr)
		rawDocs = nil
	}
	existingDocs := filterTopicDocs(rawDocs)
	communityOf, perr := partitionFromPersisted(ctx, p.gc)
	if perr != nil {
		rep.addStageError("partition read failed", perr)
		communityOf = map[string]string{}
	}

	// Build the topic set (one Topic per cluster) and run the merge cascade.
	topics := buildTopicsFromClusters(clusters, createdAt, contentByCluster)

	// Populate each singleton topic's SummaryVector from its topic doc's already-
	// drained pipeline embedding (a pure in-memory medoid→docID→vectorIndex join, no
	// round trip). Done BEFORE the cascade so RunMergeCascade's groupEmbedding reads
	// the summary vector in the first comparison round, and the post-cascade link
	// pass + survey inherit it on un-merged survivors; a merged-union topic gets
	// SummaryVector=nil from unionTopics and correctly falls back to its centroid.
	populateSummaryVectors(topics, existingDocs, vectorIndex)

	merged, chains := RunMergeCascade(topics, vectorIndex, createdAt, mergeThreshold)
	rep.MergeChains = chains
	rep.TopicCount = len(merged)

	// Coverage: count post-cascade survivors carrying a usable summary vector — the
	// ones whose grouping preferred the summary vector over the centroid. Merged-
	// union topics dropped to nil SummaryVector, so they fall out of this count.
	for _, tp := range merged {
		if len(tp.SummaryVector) == vectorBytes {
			rep.SummaryVectorBacked++
		}
	}

	// Reconcile existing docs against the cascade unions + live partition.
	reconcile, rerr := reconcileTopicDocs(ctx, p.gc, existingDocs, communityOf, unionsFromCascade(merged))
	if rerr != nil {
		rep.addStageError("reconciliation failed", rerr)
	}
	rep.Rekeyed, rep.Merged, rep.Tombstoned = reconcile.Rekeyed, reconcile.Merged, reconcile.Tombstoned
	rep.TombstonedDocs = reconcile.tombstoned // id+name list for the loud report (hard delete)

	// Topic-summary create for newly-eligible topics. Skipped when no summarizer.
	create, cerr := createTopicDocs(ctx, p.gc, merged, medoidSetFromDocs(existingDocs), stablePrimaryLabels(communityOf), p.summarizer)
	if cerr != nil {
		rep.addStageError("topic create failed", cerr)
	}
	rep.SummariesCreated = create.Created

	// Drift re-summary over the post-cascade topic set. Skipped when no
	// summarizer (driftTopicDocs returns a zero report).
	drift, dferr := driftTopicDocs(ctx, p.gc, existingDocs, topicsByMedoid(merged), p.summarizer)
	if dferr != nil {
		rep.addStageError("drift re-summary failed", dferr)
	}
	rep.SummariesRefreshed = drift.Resummaried

	// Link pass over the SURVIVING topics — runs even with a nil LLM (centroid-
	// fallback group embedding), so links are materialized regardless.
	link, lerr := MaterializeLinks(ctx, p.gc, RunGroupSimilarity(merged, linkThreshold))
	if lerr != nil {
		rep.addStageError("link write failed", lerr)
	}
	rep.LinksCreated = link.Created
	rep.AlreadyLinked = link.AlreadyLinked

	// Threshold-tuning survey over the same post-cascade topic set (read-only): the
	// histogram + near-miss pairs below the link threshold inform the next threshold
	// lowering.
	rep.SimBuckets, rep.NearMisses = SurveyGroupSimilarity(merged, linkThreshold)

	// Densification pass — runs AFTER the link pass over the SAME surviving post-cascade
	// topics, reusing the already-drained in-memory vectorIndex (NO re-drain). It needs
	// only vectors + member sets (no summarizer/embedder), so it runs in the partial-
	// degrade band too. Because links wrote first (committed synchronously), a densify
	// pair that coincides with a fresh medoid link is suppressed by the idempotency
	// pre-read.
	p.runDensifyPhase(ctx, merged, vectorIndex, densify, rep)

	// Structural-clique passes — FINAL stage, over the RAW clusters (thought set) not the
	// cascade topics, LAST so the idempotency pre-reads see committed edges. ONE shared
	// resolution (resolveArtifactsAndRoots) feeds BOTH the tree-link clique and, STRICTLY
	// AFTER it, the artifact-link clique (thoughts sharing a real standalone artifact that
	// is NOT work-item-rooted — the complement of tree-link). Artifact-link runs after
	// tree-link so its idempotency pre-read sees tree-link's committed edges. (WHY:
	// runStructuralCliquePhases.)
	p.runStructuralCliquePhases(ctx, clusters, rep)
}

// memberCreatedAtAndContent hydrates every cluster member ONCE to derive (a) the
// per-member CreatedAt the cascade uses for medoid/topic tie-breaks and (b) the
// per-cluster summary prompt content (joined member symbol-names).
func (p *PropagationLoop) memberCreatedAtAndContent(ctx context.Context, clusters []ThoughtCluster) (map[string]int64, map[string]string) {
	var allMembers []string
	for _, c := range clusters {
		allMembers = append(allMembers, c.ThoughtIDs...)
	}
	nodeByID := fetchNodesByIDs(ctx, p.gc, allMembers)
	createdAt := make(map[string]int64, len(nodeByID))
	for id, n := range nodeByID {
		createdAt[id] = n.GetCreatedAt()
	}
	content := make(map[string]string, len(clusters))
	for _, c := range clusters {
		names := make([]string, 0, len(c.ThoughtIDs))
		for _, id := range c.ThoughtIDs {
			if n, ok := nodeByID[id]; ok && n.GetSymbolName() != "" {
				names = append(names, n.GetSymbolName())
			}
		}
		content[c.ID] = strings.Join(names, "; ")
	}
	return createdAt, content
}

// buildTopicsFromClusters lifts each cluster into a singleton Topic — the merge
// cascade's input. A topicless cluster enters as a one-cluster topic; the cascade
// unions them as similarity dictates.
func buildTopicsFromClusters(clusters []ThoughtCluster, createdAt map[string]int64, contentByCluster map[string]string) []Topic {
	topics := make([]Topic, 0, len(clusters))
	for _, c := range clusters {
		// Topic CreatedAt = the minimum member CreatedAt (the oldest thought anchors it).
		var minCreated int64
		for i, id := range c.ThoughtIDs {
			ca := createdAt[id]
			if i == 0 || ca < minCreated {
				minCreated = ca
			}
		}
		topics = append(topics, Topic{
			PrimaryClusterID: c.ID,
			MemberClusters:   []string{c.ID},
			MemberThoughtIDs: append([]string(nil), c.ThoughtIDs...),
			Centroid:         c.Centroid,
			MedoidID:         c.MedoidID,
			CreatedAt:        minCreated,
			Size:             c.Size,
			SummaryContent:   contentByCluster[c.ID],
		})
	}
	return topics
}

// unionsFromCascade builds the reconciliation union map (survivor medoid →
// topicUnion) from the post-cascade topics: every topic spanning more than one
// member cluster is a cascade survivor whose doc must be re-keyed + its
// member_clusters rewritten. MergedAwayMedoids are left empty here — reconcile
// keys losers by communityOf mapping a doc's medoid to a live cluster that now
// belongs to a survivor; the survivor entry is keyed by the survivor medoid so a
// loser doc whose medoid != the survivor's is tombstoned as an orphan-or-loser.
//
// NOTE: a merged-away topic's medoid maps (via communityOf) to the survivor's
// live cluster, so its doc is recognized as belonging to a topic it no longer
// anchors and is tombstoned by reconcile (its medoid is not a survivor key).
func unionsFromCascade(merged []Topic) map[string]topicUnion {
	unions := map[string]topicUnion{}
	for _, tp := range merged {
		if len(tp.MemberClusters) <= 1 || tp.MedoidID == "" {
			continue // an un-merged singleton topic needs no union rewrite
		}
		unions[tp.MedoidID] = topicUnion{
			PrimaryClusterID: tp.PrimaryClusterID,
			MemberClusters:   tp.MemberClusters,
		}
	}
	return unions
}

// medoidSetFromDocs returns the set of medoid IDs already anchoring a topic doc.
func medoidSetFromDocs(docs []*knowledgev1.Node) map[string]bool {
	out := map[string]bool{}
	for _, d := range docs {
		if m := kgtypes.Value(d, metaMedoidID); m != "" {
			out[m] = true
		}
	}
	return out
}

// stablePrimaryLabels returns the set of cluster_ids that are persisted (already
// assigned in the live partition) — a topic whose primary label is in this set is
// stable enough to earn a summary (a brand-new-this-pass label is not).
func stablePrimaryLabels(communityOf map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, cid := range communityOf {
		out[cid] = true
	}
	return out
}

// topicsByMedoid indexes the post-cascade topics by their medoid for the drift
// stage (which matches existing docs to their live topic by medoid_id).
func topicsByMedoid(topics []Topic) map[string]Topic {
	out := make(map[string]Topic, len(topics))
	for _, tp := range topics {
		if tp.MedoidID != "" {
			out[tp.MedoidID] = tp
		}
	}
	return out
}
