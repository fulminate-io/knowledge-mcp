// SPDX-License-Identifier: Apache-2.0

package thought

import "log/slog"

// similarity_report.go holds the SimilarityReport carrier — the loud result schema of a
// lever invocation — plus its per-stage error seam and the small report-row structs
// (TombstonedDoc, TopicDensifyStat). Split out of similarity_lever.go to keep that file
// under the 500-line cap; the lever body (RunSimilarityPass / runTopicPipeline) stays
// there. The tree-link / artifact-link per-clique stat rows (TreeLinkTreeStat /
// ArtifactLinkStat) live with their phases (tree_link_write.go / artifact_link_write.go).

// SimilarityReport is the loud result of a lever invocation. It surfaces every
// action (links created, merge cascade chains, summaries created/refreshed,
// reconciliation counts) plus the coalesce / degraded reasons.
type SimilarityReport struct {
	Coalesced      bool   // the trigger was absorbed by an in-flight reflection pass
	CoalesceReason string // human-readable coalesce explanation
	Degraded       bool   // some bands were skipped (missing scanner/summarizer)
	DegradedNote   string // human-readable list of skipped bands

	LinkThreshold  float64
	MergeThreshold float64

	TopicCount    int             // surviving topics after the cascade
	MergeChains   []MergeChain    // cascade unions (A+B→AB, AB+C→ABC)
	LinksCreated  []LinkCandidate // medoid relates-to edges written this pass
	AlreadyLinked int             // link-candidate pairs already sharing an edge

	// SummaryVectorBacked counts the surviving topics whose grouping used a
	// summary vector (len(SummaryVector)==vectorBytes) rather than the centroid
	// fallback — operator visibility into how much of the corpus is summary-vector-
	// backed. Counted AFTER the cascade, so merged-union topics (which drop to
	// centroid) are correctly excluded.
	SummaryVectorBacked int

	Rekeyed    int // topic docs re-keyed to a shifted live label
	Merged     int // cascade survivors re-keyed + member_clusters rewritten
	Tombstoned int // merged-away losers + orphans removed

	// TombstonedDocs names every doc the reconcile tombstoned (id+name). The
	// delete is SOFT (recoverable), but the report still LISTS what was removed,
	// never a bare count — a wrongful tombstone hides the doc from every read, so
	// it must be auditable in the rendered result and the daemon log.
	TombstonedDocs []TombstonedDoc

	SummariesCreated   int // new topic-summary docs
	SummariesRefreshed int // drift-triggered re-summaries

	// Densification — the post-link within-topic kNN phase. PerTopic stats,
	// the total edges written this run, the budget-hit flag, and the loud skipped
	// reason (set when densify could not run, e.g. a nil scanner → no vector index).
	DensifyPerTopic      []TopicDensifyStat
	DensifyEdgesTotal    int
	DensifyBudgetHit     bool
	DensifyStarved       int    // topics shortchanged by the edge budget (named in the loud line)
	DensifyBudget        int    // the resolved per-run edge budget (for the loud line)
	DensifySkippedReason string // loud degraded note when densify was skipped entirely
	// Tree-link — per-work-item-tree clique phase (semantics + TreeLinkTreeStat: tree_link_write.go).
	TreeLinkPerTree       []TreeLinkTreeStat
	TreeLinkEdgesTotal    int
	TreeLinkSkippedReason string
	// Artifact-link — per-shared-artifact clique phase, AFTER tree-link, over the same
	// resolution bundle (semantics + ArtifactLinkStat: artifact_link_write.go). Covers
	// thoughts sharing a real STANDALONE artifact (decision/research/rule/finding/
	// document/question) that does NOT resolve to a work-item root — the complement of
	// tree-link.
	ArtifactLinkPerArtifact   []ArtifactLinkStat
	ArtifactLinkEdgesTotal    int
	ArtifactLinkSkippedReason string

	// StageErrors lists every per-stage failure the pipeline degraded through
	// (read/reconcile/create/drift/link/densify). Each stage already logs its
	// error and continues — but a log-only failure is invisible to the caller,
	// and "0 generated" with a hidden create failure reads as "nothing eligible".
	// The report must carry the failure, not just the count.
	StageErrors []string

	// Threshold-tuning survey: the pairwise group-similarity histogram
	// ([0.70,1.0] in 0.05 buckets) plus the top near-miss pairs just below the
	// link threshold — what the next incremental lowering would catch. The
	// thresholds start deliberately HIGH and come down by observation; without
	// this section every run only shows what PASSED, leaving the tuning blind.
	SimBuckets []SimBucket
	NearMisses []LinkCandidate
}

// addStageError records a per-stage pipeline failure on the report AND logs it —
// the single seam every degrade-and-continue stage routes through, so a failure
// is never log-only.
func (r *SimilarityReport) addStageError(stage string, err error) {
	r.StageErrors = append(r.StageErrors, stage+": "+err.Error())
	slog.Warn("thought: similarity lever — "+stage, "err", err)
}

// TombstonedDoc names one doc the reconcile hard-deleted: id + name, so the loud
// report and the daemon log can list exactly what was removed rather than a bare
// count.
type TombstonedDoc struct {
	ID   string
	Name string
}

// TopicDensifyStat is the per-touched-topic densify accounting the lever report
// renders: the stable topic key, the densify edges written for it, and the cheap
// in-pass before/after STRUCTURAL component-count estimate (a within-topic
// relates-to-subgraph proxy, NOT Leiden communities — see computeDensifyEdges).
type TopicDensifyStat struct {
	TopicKey         string
	EdgesWritten     int
	BeforeComponents int
	AfterComponents  int
}
