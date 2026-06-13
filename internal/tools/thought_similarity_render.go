// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"log/slog"
	"strings"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// renderSimilarityReport renders the lever's SimilarityReport as the loud tool
// result: links created (pairs + scores), MERGE cascade chains (A+B→AB, AB+C→ABC
// + scores), summaries generated/refreshed, reconciliation re-key/merge/tombstone
// counts, and the coalesce/degraded reasons. A coalesced pass renders only the
// reason (it created nothing).
func renderSimilarityReport(r clientthought.SimilarityReport) string {
	var sb strings.Builder
	sb.WriteString("# Topic Similarity Pass\n\n")

	if r.Coalesced {
		fmt.Fprintf(&sb, "COALESCED: %s\n", r.CoalesceReason)
		return sb.String()
	}

	fmt.Fprintf(&sb, "- Thresholds: link %.3f, merge %.3f\n", r.LinkThreshold, r.MergeThreshold)
	fmt.Fprintf(&sb, "- Surviving topics: %d\n", r.TopicCount)
	fmt.Fprintf(&sb, "- Summary-vector-backed: %d/%d topics (rest grouped by centroid fallback)\n", r.SummaryVectorBacked, r.TopicCount)
	if r.Degraded {
		fmt.Fprintf(&sb, "- DEGRADED: %s\n", r.DegradedNote)
	}
	// Per-stage failures the pipeline degraded through — rendered FIRST so a
	// "0 generated" count below is never mistaken for "nothing eligible" when the
	// truth is "the create failed".
	if len(r.StageErrors) > 0 {
		fmt.Fprintf(&sb, "\n## STAGE ERRORS (%d) — counts below reflect what SUCCEEDED, not what was attempted\n", len(r.StageErrors))
		for _, e := range r.StageErrors {
			fmt.Fprintf(&sb, "  - %s\n", e)
		}
	}
	sb.WriteString("\n")

	// Merges performed as cascade chains.
	fmt.Fprintf(&sb, "## Merges Performed (%d)\n", len(r.MergeChains))
	for _, ch := range r.MergeChains {
		fmt.Fprintf(&sb, "  - %s → %s (sim %.3f)\n", strings.Join(ch.From, "+"), ch.To, ch.Sim)
	}
	sb.WriteString("\n")

	// Links created.
	fmt.Fprintf(&sb, "## Links Created (%d, already-linked %d)\n", len(r.LinksCreated), r.AlreadyLinked)
	for _, lc := range r.LinksCreated {
		fmt.Fprintf(&sb, "  - %s ↔ %s (sim %.3f)\n", lc.MedoidA, lc.MedoidB, lc.Score)
	}
	sb.WriteString("\n")

	renderSimSurvey(&sb, r)

	// Summaries + reconciliation accounting.
	fmt.Fprintf(&sb, "## Summaries: %d generated, %d refreshed (drift)\n", r.SummariesCreated, r.SummariesRefreshed)
	fmt.Fprintf(&sb, "## Reconciliation: %d re-keyed, %d merged-survivor, %d tombstoned\n",
		r.Rekeyed, r.Merged, r.Tombstoned)
	renderTombstonedDocs(&sb, r.TombstonedDocs)

	renderDensifySection(&sb, r)
	renderTreeLinkSection(&sb, r)
	renderArtifactLinkSection(&sb, r)

	return sb.String()
}

// renderSimSurvey renders the threshold-tuning survey: the pairwise
// group-similarity histogram and the top near-miss pairs just below the link
// threshold. This is the instrument for the lower-the-thresholds-incrementally
// loop — without it each run only shows what PASSED the current thresholds.
func renderSimSurvey(sb *strings.Builder, r clientthought.SimilarityReport) {
	if len(r.SimBuckets) == 0 && len(r.NearMisses) == 0 {
		return
	}
	if len(r.SimBuckets) > 0 {
		sb.WriteString("## Similarity Distribution (topic pairs ≥ 0.70 — threshold tuning)\n")
		for _, b := range r.SimBuckets {
			fmt.Fprintf(sb, "  - [%.2f, %.2f): %d pairs\n", b.Lo, b.Hi, b.Count)
		}
		sb.WriteString("\n")
	}
	if len(r.NearMisses) > 0 {
		fmt.Fprintf(sb, "## Near Misses (top %d below link %.3f — what the next lower threshold would catch)\n",
			len(r.NearMisses), r.LinkThreshold)
		for _, nm := range r.NearMisses {
			fmt.Fprintf(sb, "  - %s ↔ %s (sim %.3f)\n", nm.MedoidA, nm.MedoidB, nm.Score)
		}
		sb.WriteString("\n")
	}
}

// tombstonedListCap bounds how many tombstoned id+name lines the rendered result
// shows; the FULL list always goes to the daemon log regardless of this cap.
const tombstonedListCap = 20

// renderTombstonedDocs lists every doc the reconcile tombstoned (SOFT delete —
// recoverable, but hidden from every read) by id+name — never a bare count — so
// a wrongful deletion is visible at a glance. The rendered
// tool result is capped (tombstonedListCap) with an "and N more" tail when large;
// the complete id+name list is always emitted to the daemon log so the audit
// record survives even when the result is truncated.
func renderTombstonedDocs(sb *strings.Builder, docs []clientthought.TombstonedDoc) {
	if len(docs) == 0 {
		return
	}
	fmt.Fprintf(sb, "### Tombstoned docs (soft delete, recoverable — %d):\n", len(docs))
	for i, d := range docs {
		if i >= tombstonedListCap {
			fmt.Fprintf(sb, "  - ... and %d more (see daemon log for the full list)\n", len(docs)-tombstonedListCap)
			break
		}
		name := d.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(sb, "  - %s [%s]\n", name, d.ID)
	}

	// Full id+name list to the daemon log — the audit record that survives result
	// truncation.
	pairs := make([]string, len(docs))
	for i, d := range docs {
		pairs[i] = d.ID + " (" + d.Name + ")"
	}
	slog.Warn("thought: similarity lever — reconcile tombstoned topic docs (soft delete)",
		"count", len(docs), "docs", strings.Join(pairs, ", "))
}

// renderDensifySection appends the densification report to the lever result:
// a per-touched-topic line (edges written + before/after component estimate), the
// total, a LOUD budget-hit line when the per-run edge budget truncated the run, a
// loud skipped line when densification was skipped entirely (nil scanner), and the
// honesty caveat footnote that the component counts are within-topic structural
// estimates, not Leiden communities.
func renderDensifySection(sb *strings.Builder, r clientthought.SimilarityReport) {
	sb.WriteString("\n## Densification (within-topic kNN)\n")

	if r.DensifySkippedReason != "" {
		fmt.Fprintf(sb, "  - SKIPPED: %s\n", r.DensifySkippedReason)
		return
	}

	for _, st := range r.DensifyPerTopic {
		fmt.Fprintf(sb, "  - topic %s: %d densify edges (components %d→%d)\n",
			st.TopicKey, st.EdgesWritten, st.BeforeComponents, st.AfterComponents)
	}
	fmt.Fprintf(sb, "  - Total densify edges written: %d\n", r.DensifyEdgesTotal)

	if r.DensifyBudgetHit {
		fmt.Fprintf(sb, "  - DENSIFY BUDGET HIT: edge budget %d reached; %d topics truncated — re-run with a higher densify_edge_budget or lower densify_threshold to continue topping up\n",
			r.DensifyBudget, r.DensifyStarved)
	}

	sb.WriteString("  - note: component counts are within-topic STRUCTURAL estimates over the relates-to subgraph, NOT Leiden communities (a lower-bound proxy for next reflection's fusion)\n")
}

// treeLinkRootIDShort bounds how many leading chars of a tree root id the per-tree
// line shows — enough to disambiguate without dumping the full hash.
const treeLinkRootIDShort = 8

// renderTreeLinkSection appends the tree-link clique report to the lever result: one
// LOUD per-tree line (root name + short root id + thought count + edges written this
// pass) per grouping tree, the total, and a loud SKIPPED line when the phase was
// skipped entirely. The section text is part of renderSimilarityReport's output, so it
// rides the persisted similarity-event body verbatim — no separate event wiring.
func renderTreeLinkSection(sb *strings.Builder, r clientthought.SimilarityReport) {
	sb.WriteString("\n## Tree-Link (project-tree clique)\n")

	if r.TreeLinkSkippedReason != "" {
		fmt.Fprintf(sb, "  - SKIPPED: %s\n", r.TreeLinkSkippedReason)
		return
	}

	for _, st := range r.TreeLinkPerTree {
		name := st.RootName
		if name == "" {
			name = "(unnamed)"
		}
		short := st.RootID
		if len(short) > treeLinkRootIDShort {
			short = short[:treeLinkRootIDShort]
		}
		fmt.Fprintf(sb, "  - tree %s (%s): %d thoughts, %d edges this pass\n",
			name, short, st.ThoughtCount, st.EdgesWritten)
	}
	fmt.Fprintf(sb, "  - Total tree-link edges written: %d\n", r.TreeLinkEdgesTotal)
}

// renderArtifactLinkSection appends the artifact-link clique report to the lever result:
// one LOUD per-artifact line (artifact name + short artifact id + thought count + edges
// written this pass) per qualifying shared artifact, the total, and a loud SKIPPED line
// when the phase was skipped entirely. Mirrors renderTreeLinkSection — the artifact-link
// channel is the complement of tree-link (thoughts sharing a real standalone
// decision/research/rule/finding/document/question that does NOT resolve to a work-item
// root). The section text is part of renderSimilarityReport's output, so it rides the
// persisted similarity-event body verbatim — no separate event wiring. The per-artifact
// lines double as the measurement readout (qualifying-artifact count = the line count,
// biggest = the max ThoughtCount, total clique edges = the total line).
func renderArtifactLinkSection(sb *strings.Builder, r clientthought.SimilarityReport) {
	sb.WriteString("\n## Shared-Artifact Clique (artifact-link)\n")

	if r.ArtifactLinkSkippedReason != "" {
		fmt.Fprintf(sb, "  - SKIPPED: %s\n", r.ArtifactLinkSkippedReason)
		return
	}

	for _, st := range r.ArtifactLinkPerArtifact {
		name := st.ArtifactName
		if name == "" {
			name = "(unnamed)"
		}
		short := st.ArtifactID
		if len(short) > treeLinkRootIDShort {
			short = short[:treeLinkRootIDShort]
		}
		fmt.Fprintf(sb, "  - artifact %s (%s): %d thoughts, %d edges this pass\n",
			name, short, st.ThoughtCount, st.EdgesWritten)
	}
	fmt.Fprintf(sb, "  - Total artifact-link edges written: %d\n", r.ArtifactLinkEdgesTotal)
}
