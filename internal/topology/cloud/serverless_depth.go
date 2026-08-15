// SPDX-License-Identifier: Apache-2.0

// serverless_depth.go implements ServerlessDepthAnalyzer — a cloud topology
// analyzer that measures how many infrastructure layers each serverless
// function depends on.
//
// The analyzer finds every serverless function node (Lambda, Cloud Functions,
// Cloud Run, Azure Functions), then runs a forward BFS following dependency
// edges (USES_SUBNET, ASSUMES_ROLE, MOUNTS_SECRET, ENCRYPTS_WITH, etc.)
// to measure the dependency depth and breadth of each function.
//
// Deep dependency trees make serverless functions fragile: a single
// permission change or secret rotation at depth N can silently break the
// function. The analyzer surfaces the deepest / widest functions so the
// operator can review and potentially simplify their dependency footprint.
//
// SEVERITY — fixed thresholds per the plan spec:
//
//   - depth >= 6 → Warning
//   - depth >= 4 → Notice
//   - else       → Info
//
// CONFIGURATION via req.Extra:
//
//   - "max_depth" — BFS depth cap (default 15, max 100).
//
// Event-chain edges (TARGETS, TRIGGERS, SUBSCRIBES_TO) are explicitly
// excluded — those belong to the event_chain analyzer's domain.
//
// DATA ACCESS — one foundation.FetchNodesByType(NodeCloudResource) browse
// supplies both the candidate functions and the node-ID → resource_type map
// the BFS uses to label discovered dependencies; a bulk foundation.FetchEdges
// over the cloud node set (filtered to the dependency edge types) supplies the
// forward adjacency. No per-node edge or by-id fetch.
package cloud

import (
	"context"
	"fmt"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// serverlessResourceTypes are the resource_type values that identify
// serverless function nodes across cloud providers.
var serverlessResourceTypes = map[string]bool{
	"lambda-function":             true, // AWS Lambda
	"gcp:cloudfunctions:function": true, // GCP Cloud Functions
	"gcp:run:service":             true, // GCP Cloud Run
	"azure-function":              true, // Azure Functions
}

// serverlessDependencyEdges are the edge types the forward BFS follows.
// These represent infrastructure dependencies — NOT event triggers.
var serverlessDependencyEdges = []kgtypes.EdgeType{
	kgtypes.EdgeUsesNetwork,
	kgtypes.EdgeUsesSubnet,
	kgtypes.EdgeAssumesRole,
	kgtypes.EdgeMountsSecret,
	kgtypes.EdgeMountsConfigMap,
	kgtypes.EdgeUsesSA,
	kgtypes.EdgeBoundTo,
	kgtypes.EdgeEncryptsWith,
	kgtypes.EdgeUsesSecurityGroup,
}

const (
	serverlessDefaultMaxDepth = 15
	serverlessWarningDepth    = 6
	serverlessNoticeDepth     = 4
)

// ServerlessDepthAnalyzer measures the infrastructure dependency depth and
// breadth of serverless functions. Zero-value usable; self-registers via init().
type ServerlessDepthAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (ServerlessDepthAnalyzer) Name() string { return "serverless_depth" }

// Run scopes to a single cloud account, finds serverless function nodes,
// and runs a forward BFS from each to measure dependency depth.
func (ServerlessDepthAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/serverless_depth: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud {
		return nil, fmt.Errorf("topology/serverless_depth: requires GraphCloud, got %q", req.Graph)
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/serverless_depth: req.Caller must not be nil")
	}

	nodes, err := foundation.FetchNodesByType(ctx, req.Caller, req.Graph, req.Name, kgtypes.NodeCloudResource)
	if err != nil {
		return nil, fmt.Errorf("topology/serverless_depth: fetch nodes cloud/%s: %w", req.Name, err)
	}

	maxDepth := resolveServerlessMaxDepth(req)

	// Collect all serverless function nodes and the resource-type map the BFS
	// uses to label discovered dependencies.
	resourceTypeByID := make(map[string]string, len(nodes))
	var functions []*knowledgev1.Node
	for _, n := range nodes {
		if n == nil {
			continue
		}
		rt := metaValue(n, "resource_type")
		resourceTypeByID[n.Id] = rt
		if !serverlessResourceTypes[rt] {
			continue
		}
		if req.Subset != nil && !req.Subset(n) {
			continue
		}
		functions = append(functions, n)
	}
	if len(functions) == 0 {
		return nil, nil
	}

	idx, err := buildServerlessIndex(ctx, req, nodes)
	if err != nil {
		return nil, err
	}

	findings := make([]foundation.Finding, 0, len(functions))
	for _, fn := range functions {
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("topology/serverless_depth: %w", cerr)
		}
		findings = append(findings, serverlessDepthBFS(idx, resourceTypeByID, fn, maxDepth))
	}

	sortServerlessFindings(findings)
	return foundation.TruncateTopK(findings, req.TopK), nil
}

// buildServerlessIndex fetches every dependency edge incident to the cloud
// node set in a bulk paged read and returns the in-memory forward adjacency.
func buildServerlessIndex(ctx context.Context, req foundation.Request, nodes []*knowledgev1.Node) (*edgeIndex, error) {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			ids = append(ids, n.Id)
		}
	}
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, ids, serverlessDependencyEdges)
	if err != nil {
		return nil, fmt.Errorf("topology/serverless_depth: fetch edges: %w", err)
	}
	return newEdgeIndex(edges), nil
}

// classifyServerlessSeverity maps dependency depth to severity using
// fixed thresholds.
func classifyServerlessSeverity(depth int) foundation.Severity {
	switch {
	case depth >= serverlessWarningDepth:
		return foundation.SeverityWarning
	case depth >= serverlessNoticeDepth:
		return foundation.SeverityNotice
	default:
		return foundation.SeverityInfo
	}
}

// resolveServerlessMaxDepth reads the BFS depth cap from req.Extra.
func resolveServerlessMaxDepth(req foundation.Request) int {
	v := foundation.ExtraFloat(req, "max_depth", float64(serverlessDefaultMaxDepth), func(f float64) bool {
		return f >= 1 && f <= 100
	})
	return int(v)
}

// sortServerlessFindings orders findings: deepest first, then by dependency
// count descending, then by primary evidence ID for stability.
func sortServerlessFindings(findings []foundation.Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		di := findings[i].Metrics["dependency_depth"]
		dj := findings[j].Metrics["dependency_depth"]
		if di != dj {
			return di > dj
		}
		ci := findings[i].Metrics["dependency_count"]
		cj := findings[j].Metrics["dependency_count"]
		if ci != cj {
			return ci > cj
		}
		return primaryEvidence(findings[i]) < primaryEvidence(findings[j])
	})
}

func init() {
	foundation.Register(ServerlessDepthAnalyzer{})
}
