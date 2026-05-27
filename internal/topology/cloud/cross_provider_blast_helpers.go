// SPDX-License-Identifier: Apache-2.0

package cloud

// cross_provider_blast_helpers.go implements the ServiceAccount discovery,
// IAM bridge resolution, and forward BFS walk for the cross-provider
// blast radius analyzer. Split from cross_provider_blast.go to keep both
// files under the line cap. The walk reads forward adjacency from the
// in-memory edgeIndex rather than per-node store queries.

import (
	"context"
	"fmt"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// saEntry is a discovered K8s ServiceAccount with its resolved IAM
// bridge targets. A SA may have zero (no bridge), one, or multiple
// IAM targets (e.g. both IRSA and GCP WI on a multi-cloud cluster).
type saEntry struct {
	NodeID string
	// IAMTargets are node IDs of the cloud IAM roles/SAs reachable
	// through identity bridges.
	IAMTargets []iamTarget
}

type iamTarget struct {
	NodeID string
	Bridge string // "irsa" or "gcp-workload-identity"
}

// collectServiceAccounts filters the cloud resource nodes to ServiceAccounts
// and resolves their IAM bridges from metadata.
func collectServiceAccounts(ctx context.Context, nodes []*knowledgev1.Node) []saEntry {
	var out []saEntry
	for _, n := range nodes {
		if ctx.Err() != nil {
			return out
		}
		if n == nil || metaValue(n, "resource_type") != "ServiceAccount" {
			continue
		}
		entry := saEntry{NodeID: n.Id}

		// AWS IRSA bridge: SA → IAM role via irsa_role_arn annotation.
		if roleARN := metaValue(n, "irsa_role_arn"); roleARN != "" {
			entry.IAMTargets = append(entry.IAMTargets, iamTarget{
				NodeID: roleARN,
				Bridge: "irsa",
			})
		}
		// GCP Workload Identity: SA → GCP SA via gcp_service_account.
		if gcpSA := metaValue(n, "gcp_service_account"); gcpSA != "" {
			entry.IAMTargets = append(entry.IAMTargets, iamTarget{
				NodeID: gcpSA,
				Bridge: "gcp-workload-identity",
			})
		}
		if len(entry.IAMTargets) > 0 {
			out = append(out, entry)
		}
	}
	return out
}

// crossProviderFinding runs the forward BFS from a SA's IAM targets and
// builds a Finding. Returns nil if the SA has no reachable resources.
func crossProviderFinding(
	idx *edgeIndex,
	nameByID map[string]string,
	sa saEntry,
	maxDepth int,
) *foundation.Finding {
	reachable := make(map[string]int) // nodeID → min depth
	for _, tgt := range sa.IAMTargets {
		forwardBFS(idx, tgt.NodeID, crossProviderForwardEdges, maxDepth, reachable)
	}
	if len(reachable) == 0 {
		return nil
	}

	score := float64(len(reachable))
	display := resolveName(nameByID, sa.NodeID)
	evidence := buildCrossProviderEvidence(sa, reachable)

	bridges := bridgeSummary(sa.IAMTargets)
	summary := fmt.Sprintf(
		"K8s ServiceAccount %s reaches %d cloud resources via %s (max depth %d)",
		display, len(reachable), bridges, maxDepth,
	)

	f := foundation.Finding{
		Algorithm: "cross_provider_blast",
		Severity:  foundation.SeverityInfo,
		Title:     fmt.Sprintf("Cross-provider blast: %s", display),
		Summary:   summary,
		Evidence:  evidence,
		Metrics: map[string]float64{
			"blast_score":    score,
			"total_affected": float64(len(reachable)),
			"bridge_count":   float64(len(sa.IAMTargets)),
		},
	}
	return &f
}

// resolveName returns the display name (SymbolName) for nodeID from the
// in-memory node-name map, falling back to the raw nodeID. Wire twin of the
// prior ResolveNodeName, resolved from the already-fetched node set instead
// of a per-node store query.
func resolveName(nameByID map[string]string, nodeID string) string {
	if name := nameByID[nodeID]; name != "" {
		return name
	}
	return nodeID
}

// forwardBFS walks outgoing edges from a starting node up to maxDepth,
// recording every reachable node and its minimum distance. Merges into
// an existing visited map so multiple IAM targets accumulate correctly.
func forwardBFS(
	idx *edgeIndex,
	start string,
	edgeTypes []kgtypes.EdgeType,
	maxDepth int,
	visited map[string]int,
) {
	if _, seen := visited[start]; !seen {
		visited[start] = 0
	}
	frontier := []string{start}
	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, nodeID := range frontier {
			for _, to := range idx.outgoing(nodeID, edgeTypes) {
				if _, seen := visited[to]; seen {
					continue
				}
				visited[to] = depth
				next = append(next, to)
			}
		}
		frontier = next
	}
}

// buildCrossProviderEvidence builds the evidence slice: SA, IAM targets,
// then top-N reachable resources sorted by distance (closest first).
func buildCrossProviderEvidence(sa saEntry, reachable map[string]int) []string {
	evidence := make([]string, 0, 1+len(sa.IAMTargets)+crossProviderEvidenceMax)
	evidence = append(evidence, sa.NodeID)
	for _, tgt := range sa.IAMTargets {
		evidence = append(evidence, tgt.NodeID)
	}

	type distNode struct {
		id   string
		dist int
	}
	ranked := make([]distNode, 0, len(reachable))
	for id, dist := range reachable {
		if id == sa.NodeID {
			continue
		}
		skip := false
		for _, tgt := range sa.IAMTargets {
			if id == tgt.NodeID {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		ranked = append(ranked, distNode{id, dist})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].dist != ranked[j].dist {
			return ranked[i].dist < ranked[j].dist
		}
		return ranked[i].id < ranked[j].id
	})
	for i, r := range ranked {
		if i >= crossProviderEvidenceMax {
			break
		}
		evidence = append(evidence, r.id)
	}
	return evidence
}

// bridgeSummary formats the bridge types for the finding summary.
func bridgeSummary(targets []iamTarget) string {
	seen := map[string]bool{}
	for _, t := range targets {
		seen[t.Bridge] = true
	}
	bridges := make([]string, 0, len(seen))
	for b := range seen {
		bridges = append(bridges, b)
	}
	sort.Strings(bridges)
	if len(bridges) == 1 {
		return bridges[0]
	}
	return fmt.Sprintf("%v", bridges)
}

// applyCrossProviderSeverity recomputes Finding.Severity using
// percentile-based ranking across all SA findings. With a single
// finding, severity stays as SeverityInfo.
func applyCrossProviderSeverity(findings []foundation.Finding) {
	if len(findings) < 2 {
		return
	}
	scores := make([]float64, len(findings))
	for i, f := range findings {
		scores[i] = f.Metrics["blast_score"]
	}
	sort.Float64s(scores)
	for i := range findings {
		pct := foundation.Percentile(scores, findings[i].Metrics["blast_score"])
		findings[i].Severity = foundation.SeverityFromPercentile(pct)
		findings[i].Metrics["percentile"] = pct
	}
}
