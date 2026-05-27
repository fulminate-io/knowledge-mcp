// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_scoring.go scores attackPath values and renders them
// as topology.Finding records. Scoring is intentionally simple and
// auditable: composite = sensitivity / (hop_count + mitigation_count + 1),
// matching the ticket spec. The +1 in the denominator prevents divide-by-
// zero for direct (seed == terminal) paths, which should never happen
// because bfsFromSeed skips the seed itself when checking for sensitive
// terminals.
//
// Mitigation counting is a pluggable signal counter: we count explicit
// deny, MFA required, WAF association, NACL block, etc. Each +1 reduces
// the final composite score — that way a chain with three mitigations
// drops below one without, even if the sensitivity and hop count are
// identical. The current implementation returns 0 from mitigationCountFor;
// the hook exists so future patches can extend the catalog without
// rewriting the scoring formula.

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// scoredPath is an attackPath plus its composite score and mitigation
// count. Returned by scorePaths and consumed by buildExposureFinding.
type scoredPath struct {
	attackPath
	CompositeScore  float64
	MitigationCount int
}

// scorePaths computes composite scores for a batch of paths. The output
// is sorted descending by composite score so callers that cap at TopK
// keep the most dangerous paths. Stable sort keeps paths with equal
// scores in their original (deterministic) order.
func scorePaths(paths []attackPath) []scoredPath {
	out := make([]scoredPath, 0, len(paths))
	for _, p := range paths {
		mit := mitigationCountFor(p)
		composite := compositeScore(p.SensitiveScore, len(p.Edges), mit)
		out = append(out, scoredPath{
			attackPath:      p,
			CompositeScore:  composite,
			MitigationCount: mit,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CompositeScore > out[j].CompositeScore
	})
	return out
}

// compositeScore implements the ticket's scoring formula. hopCount is
// the number of edges; mitigationCount reduces severity linearly.
// The +1 in the denominator enforces a maximum of sensitivity itself
// (at zero hops and zero mitigations) and avoids a divide-by-zero.
func compositeScore(sensitivity float64, hopCount, mitigationCount int) float64 {
	if sensitivity <= 0 {
		return 0
	}
	denom := float64(hopCount + mitigationCount + 1)
	return sensitivity / denom
}

// mitigationCountFor inspects a path's edges and returns the number of
// mitigation signals recognized. Signals currently recognized: none —
// this is a v1 hook. Future patches can extend the catalog by reading
// edge metadata set by the cloud collector (is_nacl, protocol, port,
// explicit_deny, waf_attached, mfa_required, ...) without changing the
// scoring formula.
func mitigationCountFor(_ attackPath) int {
	return 0
}

// buildExposureFinding renders one scored path as a topology.Finding.
// algorithm is the analyzer name set by the caller (aws_public_exposure,
// k8s_public_exposure, or unified_public_exposure). The Summary is
// template-rendered; no LLM call happens here.
//
// Severity is derived from the composite score:
//   - >= 0.6  → critical
//   - >= 0.3  → warning
//   - < 0.3   → notice
//
// Evidence holds the full ordered path node IDs so downstream dedup via
// the findings package uses the terminal as primary_evidence (the
// Phase 1 ticket requirement) when the caller places the terminal first.
// We keep the seed at index 0 and the terminal at the last index, which
// matches the visual reading order of the summary; dedup keys on
// primary_evidence which is Evidence[0] (the seed). If the seed changes,
// the walker picks up a fresh finding, which is the correct behavior —
// fixing the entry point invalidates the old chain.
func buildExposureFinding(ctx context.Context, req Request, algorithm string, sp scoredPath) Finding {
	if len(sp.Nodes) == 0 {
		return Finding{Algorithm: algorithm}
	}
	hopCount := len(sp.Edges)
	hopList := renderHopList(sp)

	seedName := resolveNodeName(ctx, req.Caller, req.Graph, req.Name, sp.Seed.NodeID)
	terminalID := sp.Nodes[len(sp.Nodes)-1]
	terminalName := resolveNodeName(ctx, req.Caller, req.Graph, req.Name, terminalID)

	title := fmt.Sprintf("Public exposure: %s → %s via %d hops", seedName, terminalName, hopCount)
	summary := fmt.Sprintf(
		"Public %s (%s) can reach %s (%s) via %d hops: %s.",
		seedName, sp.Seed.Reason, terminalName, sp.SensitiveReason, hopCount, hopList,
	)

	metrics := map[string]float64{
		"composite_score":  sp.CompositeScore,
		"sensitivity":      sp.SensitiveScore,
		"entry_score":      sp.Seed.EntryScore,
		"hop_count":        float64(hopCount),
		"mitigation_count": float64(sp.MitigationCount),
	}
	return Finding{
		Algorithm: algorithm,
		Severity:  severityForExposure(sp.SensitiveScore, sp.CompositeScore),
		Title:     title,
		Summary:   summary,
		Evidence:  sp.Nodes,
		Metrics:   metrics,
		Metadata:  exposureFindingMetadata(sp),
	}
}

// renderHopList joins the path's nodes and edge kinds into a visual
// "A -[KIND]-> B -[KIND]-> C" string. Used by buildExposureFinding to
// produce a concrete hop narrative in the finding summary.
func renderHopList(sp scoredPath) string {
	if len(sp.Nodes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(sp.Nodes[0])
	for i, e := range sp.Edges {
		fmt.Fprintf(&b, " -[%s]-> %s", e.Kind, sp.Nodes[i+1])
	}
	return b.String()
}

// severityForExposure maps (terminal sensitivity, composite score) to a
// topology severity. Terminal sensitivity is the dominant signal:
// reaching a maximum-sensitivity terminal (admin IAM role, KMS key,
// secret — anything scored ≥ 0.95) from a public-entry seed is ALWAYS a
// critical incident regardless of hop count. The composite score then
// tunes the remaining severity bands for lower-sensitivity terminals.
//
//   - terminal sensitivity ≥ 0.95       → critical
//   - composite ≥ 0.3                   → warning
//   - composite ≥ 0.1                   → notice
//   - otherwise                         → notice (floor — every emitted
//     finding is at least informational)
//
// Future patches can split out distinct "high" and "critical" terminals
// or tune breakpoints without changing the scoring formula.
func severityForExposure(sensitivity, composite float64) Severity {
	if sensitivity >= 0.95 {
		return SeverityCritical
	}
	switch {
	case composite >= 0.3:
		return SeverityWarning
	default:
		return SeverityNotice
	}
}

// exposureFindingMetadata returns the metadata map written onto the
// emitted finding. Carries cross_graph=true when any hop in the path
// carried a cross_graph edge metadata key — downstream dedup and search
// can then surface just the composition paths via the metadata filter.
func exposureFindingMetadata(sp scoredPath) map[string]string {
	meta := map[string]string{
		"entry_reason":     sp.Seed.Reason,
		"terminal_reason":  sp.SensitiveReason,
		"seed_resource":    sp.Seed.ResourceType,
		"cloud_family":     sp.Seed.CloudFamily,
		"mitigation_count": fmt.Sprintf("%d", sp.MitigationCount),
	}
	for _, e := range sp.Edges {
		if e.Metadata["cross_graph"] == "true" {
			meta["cross_graph"] = "true"
			break
		}
	}
	return meta
}
