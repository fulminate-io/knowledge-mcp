// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_escalation_finding.go renders escalation paths as topology Findings.
// Split out from iam_escalation_paths.go to keep both files under the
// 300-line production cap. The BFS and path-reconstruction logic lives in
// iam_escalation_paths.go; this file only consumes escalationPath values
// and produces Finding values.

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// buildEscalationFinding renders one escalation path as a Finding.
//
// Severity is always Critical — privilege escalation chains, even short ones,
// are high-impact security findings. The Metrics map carries:
//
//   - path_length:        number of nodes in the path (source + intermediates + admin)
//   - hop_count:          number of edges in the path
//   - has_cross_account:  1 if any node ARN belongs to a different AWS account
//   - min_confidence:     the LOWEST per-edge confidence along the path
//     (weakest-link model). Phase 9 Step 3 merges findings that share the
//     same (source, terminal) key; min_confidence is updated to the minimum
//     across all contributing rules during the merge.
func buildEscalationFinding(ctx context.Context, req Request, p escalationPath) Finding {
	hops := len(p.Edges)
	srcName := resolveNodeName(ctx, req.Caller, kgtypes.GraphCloud, req.Name, p.Source)
	title := fmt.Sprintf("IAM privilege escalation: %s → admin via %d hops", srcName, hops)
	summary := buildPMapperNarrative(req, p)

	cross := pathHasCrossAccount(req.Name, p)
	metrics := map[string]float64{
		"path_length":    float64(len(p.Nodes)),
		"hop_count":      float64(hops),
		"min_confidence": pathMinConfidence(p.Edges),
	}
	if cross {
		metrics["has_cross_account"] = 1
	}
	return Finding{
		Algorithm: "iam_escalation",
		Severity:  SeverityCritical,
		Title:     title,
		Summary:   summary,
		Evidence:  collectEvidence(p),
		Metrics:   metrics,
	}
}

// collectEvidence returns the node ID list that becomes the finding's
// Evidence field. Source first, then every subsequent node in the path.
// Split out so Step 3 dedup can extend the evidence with contributing
// rule names without bloating buildEscalationFinding.
func collectEvidence(p escalationPath) []string {
	out := make([]string, 0, len(p.Nodes))
	out = append(out, p.Source)
	out = append(out, p.Nodes[1:]...)
	return out
}

// pathMinConfidence returns the minimum Confidence across every edge in a
// path. Missing/zero values are treated as 1.0 so older tests that never
// set Confidence continue to produce 1.0 metrics and so the minimum is
// only pulled down by rules that explicitly declared lower confidence.
// Empty paths return 1.0.
func pathMinConfidence(edges []iamInferredEdge) float64 {
	min := 1.0
	for _, e := range edges {
		c := e.Confidence
		if c <= 0 {
			c = 1.0
		}
		if c < min {
			min = c
		}
	}
	return min
}

// pathHasCrossAccount returns true if the path transitions between two
// different AWS accounts at any hop. Uses the Accounts slice populated
// by reconstructPaths when available and falls back to ARN parsing on
// the Nodes slice for legacy paths that don't carry account tuples.
func pathHasCrossAccount(currentAccount string, p escalationPath) bool {
	if len(p.Accounts) > 0 {
		first := p.Accounts[0]
		for _, a := range p.Accounts[1:] {
			if a != "" && a != first {
				return true
			}
		}
		for _, a := range p.Accounts {
			if a != "" && a != currentAccount {
				return true
			}
		}
		return false
	}
	for _, n := range p.Nodes {
		acct := accountFromARN(n)
		if acct != "" && acct != currentAccount {
			return true
		}
	}
	return false
}
