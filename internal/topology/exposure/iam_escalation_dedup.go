// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_escalation_dedup.go implements Phase 9 Step 3: collapse multiple
// paths that share the same (source_principal, terminal_admin) tuple into
// a single finding whose Evidence enumerates every contributing rule and
// whose min_confidence metric tracks the LOWEST confidence across all
// contributing rules.
//
// Why this matters: the same principal can reach the same admin target
// via multiple independent rules — e.g., a developer might be able to
// attach a policy to admin-role via iam:PutRolePolicy AND rewrite its
// policy set via iam:CreatePolicyVersion. The BFS in
// iam_escalation_paths.go currently records only the first rule that
// provides an edge; OQ-3 requires we enumerate all of them in one
// finding instead of emitting N duplicate findings (one per rule).
//
// Keying: (source_principal_arn, terminal_state_arn). The source is the
// BFS seed; the terminal is the admin node the escalation reaches. Two
// paths with different targets are still separate findings even if they
// share a source — the user needs to see each distinct admin state as
// its own attack surface.
//
// Merge semantics:
//   - Evidence: the first path's [source, ...path nodes] list, then a
//     suffix of "rule:<name>" tokens sorted alphabetically, one per
//     distinct contributing rule. The rule tokens live after the node
//     list so existing primaryEvidence() callers and has_cross_account
//     detection continue to work unchanged.
//   - min_confidence: min across every contributing rule's confidence.
//     Dead-simple weakest-link model; matches how BFS hop-count dedup
//     should surface the weakest claim.
//   - Title/Summary: taken from the first merged path. The narrative
//     still describes a concrete chain — it just happens to be one of
//     several possible routes. Adding "OR <rule>" branches to the
//     narrative belongs in a follow-up if users ask for it.
//
// Contributing rules are collected from two sources:
//  1. Every edge in the BFS-reconstructed path (path.Edges[].RuleName).
//  2. Every edge in inferred[source] whose ToID == target. This catches
//     the "multiple rules emit the same direct edge" case where the BFS
//     arbitrarily picked one.
//
// Self-loop rules (attach_policy / effective_admin on a terminal) are
// already flattened into the admin set before BFS runs, so they don't
// contribute additional rule tokens here — the admin set loses
// per-self-loop rule provenance and we accept that v1 limitation rather
// than reshape the admin set. A future Phase 10 could track
// per-admin-target rule provenance if users demand it.

import (
	"context"
	"sort"
)

// findingKey identifies the (source, terminal) tuple used for Step 3
// dedup. Strings are source and target node IDs exactly as they appear
// in the escalationPath — no normalization, so cross-account ARNs
// dedup correctly against themselves and only themselves.
type findingKey struct {
	Source string
	Target string
}

// dedupFindings merges escalation paths that share the same (source,
// terminal) tuple, enumerating every contributing rule in Evidence and
// tracking the LOWEST per-rule confidence as min_confidence. Called
// from Run in place of the one-path-one-finding loop.
//
// Output ordering is preserved by path insertion order so
// sortEscalationFindings produces deterministic ranking. Findings with
// unique keys pass through unchanged (single-path dedup is a no-op).
func dedupFindings(ctx context.Context, req Request, paths []escalationPath, inferred map[string][]iamInferredEdge) []Finding {
	byKey := make(map[findingKey]int)
	rulesByKey := make(map[findingKey]map[string]float64)
	var findings []Finding

	for _, p := range paths {
		key := findingKey{Source: p.Source, Target: p.Target}
		contributing := collectContributingRules(p, inferred)
		if idx, ok := byKey[key]; ok {
			mergeContributing(rulesByKey[key], contributing)
			applyRulesToFinding(&findings[idx], rulesByKey[key])
			continue
		}
		// First path for this key — seed the finding.
		f := buildEscalationFinding(ctx, req, p)
		byKey[key] = len(findings)
		rulesByKey[key] = make(map[string]float64)
		mergeContributing(rulesByKey[key], contributing)
		findings = append(findings, f)
		applyRulesToFinding(&findings[len(findings)-1], rulesByKey[key])
	}
	return findings
}

// collectContributingRules returns the per-rule confidence for every
// rule that contributed an edge along the path OR in inferred[source]
// whose target matches the path's terminal. Zero-confidence entries are
// treated as 1.0 (matches pathMinConfidence) so legacy test fixtures
// that never set confidence still produce sensible defaults.
func collectContributingRules(p escalationPath, inferred map[string][]iamInferredEdge) map[string]float64 {
	out := make(map[string]float64)
	recordRule := func(name string, conf float64) {
		if name == "" {
			return
		}
		if conf <= 0 {
			conf = 1.0
		}
		if existing, ok := out[name]; !ok || conf < existing {
			out[name] = conf
		}
	}
	for _, e := range p.Edges {
		recordRule(e.RuleName, e.Confidence)
	}
	// Scan inferred for alternative rules that also reach the target
	// directly from the source. This catches the "two rules emit the
	// same edge, BFS picked one" scenario.
	for _, e := range inferred[p.Source] {
		if e.ToID != p.Target {
			continue
		}
		recordRule(e.RuleName, e.Confidence)
	}
	return out
}

// mergeContributing folds src into dst, keeping the minimum confidence
// per rule name. Called once per path during dedupFindings.
func mergeContributing(dst, src map[string]float64) {
	for name, conf := range src {
		if existing, ok := dst[name]; !ok || conf < existing {
			dst[name] = conf
		}
	}
}

// applyRulesToFinding writes the current contributing-rule map back
// onto the finding: Evidence gains "rule:<name>" tokens (sorted) and
// min_confidence is recomputed as the minimum across all contributing
// rules. Called every time a new path merges into an existing key so
// the finding stays in sync with the accumulated contributor set.
func applyRulesToFinding(f *Finding, rules map[string]float64) {
	if len(rules) == 0 {
		return
	}
	names := make([]string, 0, len(rules))
	minConf := 1.0
	for name, conf := range rules {
		names = append(names, name)
		if conf > 0 && conf < minConf {
			minConf = conf
		}
	}
	sort.Strings(names)

	// Evidence: drop any previously-appended rule tokens, then append
	// the refreshed sorted set. Rule tokens are identified by the
	// "rule:" prefix so re-merges stay idempotent.
	trimmed := make([]string, 0, len(f.Evidence))
	for _, ev := range f.Evidence {
		if len(ev) >= 5 && ev[:5] == "rule:" {
			continue
		}
		trimmed = append(trimmed, ev)
	}
	for _, name := range names {
		trimmed = append(trimmed, "rule:"+name)
	}
	f.Evidence = trimmed

	if f.Metrics == nil {
		f.Metrics = map[string]float64{}
	}
	f.Metrics["min_confidence"] = minConf
	f.Metrics["contributing_rules"] = float64(len(names))
}
