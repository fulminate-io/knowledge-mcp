// SPDX-License-Identifier: Apache-2.0

package cloud

// cross_provider_blast.go implements CrossProviderBlastAnalyzer — traces
// K8s ServiceAccount → cloud IAM role → reachable cloud resources to
// quantify the blast radius of cross-provider identity bridges (AWS IRSA,
// GCP Workload Identity).
//
// The analyzer answers: "if this K8s ServiceAccount is compromised, how
// many cloud resources does the attacker reach through the IAM bridge?"
//
// BRIDGES. Two identity mechanisms are supported:
//   - AWS IRSA: SA metadata "irsa_role_arn" → IAM role ARN. The ARN is used
//     as a cloud node ID directly.
//   - GCP Workload Identity: SA metadata "gcp_service_account" → GCP SA
//     email. The GCP SA node ID is the email itself (emitted by the GCP
//     IAM collector as resource_type "gcp:iam:serviceAccount").
//
// FORWARD BFS. From each resolved IAM role / GCP SA, the analyzer walks
// outgoing cloud edges (TARGETS, ASSUMES_ROLE, GRANTS, ENCRYPTS_WITH,
// BOUND_TO, MOUNTS_SECRET, USES_NETWORK) to count reachable resources.
//
// SEVERITY. Percentile-based across all ServiceAccounts in the graph,
// using SeverityFromPercentile. A single SA returns SeverityInfo.
//
// LAYERING. This package must not import cloud/k8s/ — metadata keys are
// string constants duplicated locally.
//
// DATA ACCESS — one foundation.FetchNodesByType(NodeCloudResource) browse
// supplies the candidate ServiceAccounts and the node-ID → name map used to
// label findings; a bulk foundation.FetchEdges over the cloud node set
// (filtered to the forward edge types) supplies the adjacency the BFS walks
// in-memory. No per-node edge or by-id fetch.

import (
	"context"
	"fmt"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

const (
	crossProviderMaxDepthDefault = 8
	crossProviderEvidenceMax     = 10
)

// crossProviderForwardEdges are the cloud edge types followed in the
// forward BFS from the IAM role to discover reachable resources.
var crossProviderForwardEdges = []kgtypes.EdgeType{
	kgtypes.EdgeTargets,
	kgtypes.EdgeAssumesRole,
	kgtypes.EdgeGrants,
	kgtypes.EdgeEncryptsWith,
	kgtypes.EdgeBoundTo,
	kgtypes.EdgeMountsSecret,
	kgtypes.EdgeUsesNetwork,
	kgtypes.EdgeUsesSubnet,
	kgtypes.EdgeUsesSecurityGroup,
	kgtypes.EdgeWorkloadIdentity,
}

// CrossProviderBlastAnalyzer traces K8s ServiceAccount → cloud IAM
// bridges → reachable cloud resources.
type CrossProviderBlastAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (CrossProviderBlastAnalyzer) Name() string { return "cross_provider_blast" }

// Run finds all K8s ServiceAccount nodes, resolves their IAM bridges,
// and computes a blast score per SA.
func (a CrossProviderBlastAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/cross_provider_blast: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud {
		return nil, nil
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/cross_provider_blast: req.Caller must not be nil")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("topology/cross_provider_blast: req.Name must not be empty")
	}

	nodes, err := foundation.FetchNodesByType(ctx, req.Caller, req.Graph, req.Name, kgtypes.NodeCloudResource)
	if err != nil {
		return nil, fmt.Errorf("topology/cross_provider_blast: fetch nodes cloud/%s: %w", req.Name, err)
	}

	sas := collectServiceAccounts(ctx, nodes)
	if len(sas) == 0 {
		return nil, nil
	}

	idx, nameByID, err := buildCrossProviderIndex(ctx, req, nodes)
	if err != nil {
		return nil, err
	}

	maxDepth := extractExtraInt(req.Extra, "max_depth", crossProviderMaxDepthDefault, 100)
	findings := make([]foundation.Finding, 0, len(sas))
	for _, sa := range sas {
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("topology/cross_provider_blast: %w", cerr)
		}
		f := crossProviderFinding(idx, nameByID, sa, maxDepth)
		if f != nil {
			findings = append(findings, *f)
		}
	}

	applyCrossProviderSeverity(findings)

	sort.SliceStable(findings, func(i, j int) bool {
		return findings[i].Metrics["blast_score"] > findings[j].Metrics["blast_score"]
	})
	return foundation.TruncateTopK(findings, req.TopK), nil
}

// buildCrossProviderIndex fetches every forward edge incident to the cloud
// node set in a bulk paged read and returns the forward adjacency plus the
// node-ID → display-name map used to label findings.
func buildCrossProviderIndex(
	ctx context.Context,
	req foundation.Request,
	nodes []*knowledgev1.Node,
) (*edgeIndex, map[string]string, error) {
	ids := make([]string, 0, len(nodes))
	nameByID := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		ids = append(ids, n.Id)
		if n.SymbolName != "" {
			nameByID[n.Id] = n.SymbolName
		}
	}
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, ids, crossProviderForwardEdges)
	if err != nil {
		return nil, nil, fmt.Errorf("topology/cross_provider_blast: fetch edges: %w", err)
	}
	return newEdgeIndex(edges), nameByID, nil
}

func init() {
	foundation.Register(CrossProviderBlastAnalyzer{})
}
