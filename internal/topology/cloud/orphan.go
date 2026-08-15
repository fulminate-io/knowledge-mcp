// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"fmt"
	"sort"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// orphan.go implements the OrphanAnalyzer — a rule-table topology analyzer
// that identifies cloud and CI/CD resources whose graph context proves they
// are unreferenced ("orphans"). One rule per resource type encodes the
// "what does it mean to be unused" predicate for that type. Per-provider
// rule files self-register via init() so adding a new resource type means
// writing one function and one register() call.
//
// ANALYZER CONTRACT — the analyzer reads its node and edge sets over the wire
// through req.Caller. It fetches every node in the scoped graph (one
// FetchAllNodes) plus every edge incident to that node set (a bulk
// FetchEdges), builds an in-memory orphanGraph, and dispatches each candidate
// node to its registered rule. The iam-role rule additionally enumerates
// other cloud graphs (foundation.FetchGraphNames) to detect cross-account
// trust relationships.
//
// CONFIDENCE — every rule returns a hardcoded confidence in [0,1]. The
// confidence is recorded as a Finding metric ("confidence") so callers can
// sort and filter without re-running the analyzer.

// orphanGraph is the in-memory view an orphan rule reads instead of a scoped
// store.DB. It wraps the bulk-fetched edge index and the node-by-ID map (the
// dead-workflow rule reads the resource_type of an edge's source node). All
// edge-presence predicates the rules call (hasOutgoing / hasIncoming /
// hasAnyOutgoing) are answered from this in-memory view — no per-node fetch.
type orphanGraph struct {
	edges    *edgeIndex
	nodeByID map[string]*knowledgev1.Node
}

// resourceType returns the resource_type metadata of the node with the given
// ID, or "" when the node is absent. Used by the dead-workflow rule to verify
// an incoming edge's source is a workflow_run.
func (g *orphanGraph) resourceType(nodeID string) string {
	return metaValue(g.nodeByID[nodeID], "resource_type")
}

// OrphanRule decides whether a single cloud resource node is orphaned. A
// rule is registered against one or more resource_type strings via
// registerOrphanRule. The dispatch loop in OrphanAnalyzer.Run iterates every
// node in the scoped graph, looks up its registered rule by resource_type,
// and invokes the rule.
//
// Parameters:
//   - ctx     — request context; rules MUST honor cancellation.
//   - caller  — the wire graph-client. Most rules ignore this and read from
//     graph; cross-account rules (iam-role) use it to enumerate other cloud
//     graphs via foundation.FetchGraphNames.
//   - account — the account name (req.Name) the analyzer is currently
//     scoping. Rules use this to label findings and to skip themselves when
//     iterating other accounts via caller.
//   - graph   — the in-memory orphanGraph for the scoped account. Rules read
//     incoming and outgoing edge presence from this view.
//   - node    — the candidate resource node being evaluated.
//
// Return values:
//   - orphan — true if the node is unused per the rule's definition.
//   - confidence — hardcoded value in [0,1] reflecting how certain the rule
//     is. Returned even when orphan=false (callers may ignore it then).
//   - summary — short human-readable explanation appended to the Finding.
//     Empty when orphan=false.
//   - err — non-nil on wire failure. The dispatch loop aborts the entire
//     analyzer run on the first non-nil error.
type OrphanRule func(
	ctx context.Context,
	caller foundation.GraphCaller,
	account string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (orphan bool, confidence float64, summary string, err error)

// orphanRules is the package-level dispatch table. Each rule file registers
// its rules at init() time via registerOrphanRule. The map is read-mostly
// after init() so we guard with an RWMutex; callers should never mutate
// the map directly.
var (
	orphanRulesMu sync.RWMutex
	orphanRules   = map[string]OrphanRule{}
)

// registerOrphanRule adds a rule for a given resource_type to the dispatch
// table. Panics if rule is nil, if resourceType is empty, or if a rule is
// already registered for the same resourceType — all three are programmer
// errors that should never reach a running server.
func registerOrphanRule(resourceType string, rule OrphanRule) {
	if rule == nil {
		panic("topology/cloud: registerOrphanRule called with nil OrphanRule")
	}
	if resourceType == "" {
		panic("topology/cloud: registerOrphanRule called with empty resourceType")
	}
	orphanRulesMu.Lock()
	defer orphanRulesMu.Unlock()
	if _, dup := orphanRules[resourceType]; dup {
		panic(fmt.Sprintf("topology/cloud: duplicate orphan rule registration: %q", resourceType))
	}
	orphanRules[resourceType] = rule
}

// lookupOrphanRule returns the rule registered for the given resource_type
// and a boolean indicating whether a rule was found.
func lookupOrphanRule(resourceType string) (OrphanRule, bool) {
	orphanRulesMu.RLock()
	defer orphanRulesMu.RUnlock()
	r, ok := orphanRules[resourceType]
	return r, ok
}

// OrphanAnalyzer implements foundation.Analyzer for the orphan-detection
// rule table. Zero-value usable; rules are registered via init() in the
// per-provider files.
type OrphanAnalyzer struct{}

// Name returns the analyzer's stable identifier. Findings emitted by Run
// carry this in their Algorithm field.
func (OrphanAnalyzer) Name() string { return "orphan" }

// Run scopes the request to a single cloud account, walks every node in
// that scope, and dispatches each node to its registered orphan rule.
// Findings are returned sorted deterministically: highest confidence first,
// then by primary evidence ID for stable tie-breaking.
//
// Behavior:
//   - req.Graph MUST be GraphCloud or GraphCICD; any other graph type
//     returns an error. The rule table is keyed by resource_type strings
//     which are graph-specific.
//   - Empty graph (no nodes) returns (nil, nil) without error.
//   - Nodes whose Type is not NodeCloudResource or NodeCICDResource are
//     silently skipped — the graph may contain proxy nodes from cross-graph
//     linkage that have no resource_type metadata.
//   - Nodes whose resource_type has no registered rule are silently skipped.
//   - req.Subset (when non-nil) filters which nodes are evaluated.
//   - req.TopK (when > 0) caps the number of returned findings.
func (a OrphanAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/orphan: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud && req.Graph != kgtypes.GraphCICD {
		return nil, fmt.Errorf("topology/orphan: requires GraphCloud or GraphCICD, got %q", req.Graph)
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/orphan: req.Caller must not be nil")
	}

	nodes, err := foundation.FetchAllNodes(ctx, req.Caller, req.Graph, req.Name)
	if err != nil {
		return nil, fmt.Errorf("topology/orphan: list nodes %s/%s: %w", req.Graph, req.Name, err)
	}
	if len(nodes) == 0 {
		return nil, nil
	}

	graph, err := buildOrphanGraph(ctx, req, nodes)
	if err != nil {
		return nil, err
	}

	findings, err := dispatchOrphanRules(ctx, req, graph, nodes)
	if err != nil {
		return nil, err
	}

	sortOrphanFindings(findings)
	return foundation.TruncateTopK(findings, req.TopK), nil
}

// buildOrphanGraph fetches every edge incident to the node set in a bulk paged
// read and returns the in-memory view the rules read.
func buildOrphanGraph(ctx context.Context, req foundation.Request, nodes []*knowledgev1.Node) (*orphanGraph, error) {
	ids := make([]string, 0, len(nodes))
	nodeByID := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		ids = append(ids, n.Id)
		nodeByID[n.Id] = n
	}
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, ids, nil)
	if err != nil {
		return nil, fmt.Errorf("topology/orphan: fetch edges: %w", err)
	}
	return &orphanGraph{edges: newEdgeIndex(edges), nodeByID: nodeByID}, nil
}

// dispatchOrphanRules walks the candidate node list, applies the subset
// filter, looks up the registered rule by resource_type, and accumulates
// orphan findings. Aborts on the first non-nil rule error so the caller
// gets a clean failure mode.
func dispatchOrphanRules(
	ctx context.Context,
	req foundation.Request,
	graph *orphanGraph,
	nodes []*knowledgev1.Node,
) ([]foundation.Finding, error) {
	findings := make([]foundation.Finding, 0, len(nodes))
	for _, n := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/orphan: %w", err)
		}
		if n == nil {
			continue
		}
		if req.Subset != nil && !req.Subset(n) {
			continue
		}
		if kgtypes.NodeType(n.Type) != kgtypes.NodeCloudResource && kgtypes.NodeType(n.Type) != kgtypes.NodeCICDResource {
			continue
		}
		resourceType := metaValue(n, "resource_type")
		if resourceType == "" {
			continue
		}
		rule, ok := lookupOrphanRule(resourceType)
		if !ok {
			continue
		}
		orphan, confidence, summary, rerr := rule(ctx, req.Caller, req.Name, graph, n)
		if rerr != nil {
			return nil, fmt.Errorf("topology/orphan: rule %q on %s: %w", resourceType, n.Id, rerr)
		}
		if !orphan {
			continue
		}
		findings = append(findings, buildOrphanFinding(req.Name, resourceType, n, confidence, summary))
	}
	return findings, nil
}

// buildOrphanFinding constructs a Finding for one orphaned resource.
// Severity is derived from confidence: high-confidence orphans surface as
// warning, lower-confidence as notice.
func buildOrphanFinding(account, resourceType string, n *knowledgev1.Node, confidence float64, summary string) foundation.Finding {
	severity := foundation.SeverityNotice
	if confidence >= 0.9 {
		severity = foundation.SeverityWarning
	}
	title := fmt.Sprintf("Orphan %s: %s", resourceType, displayName(n))
	body := summary
	if body == "" {
		body = fmt.Sprintf("Resource %s in account %s appears unused per the %s orphan rule.", displayName(n), account, resourceType)
	}
	return foundation.Finding{
		Algorithm: "orphan",
		Severity:  severity,
		Title:     title,
		Summary:   body,
		Evidence:  []string{n.Id},
		Metrics: map[string]float64{
			"confidence": confidence,
		},
	}
}

// sortOrphanFindings orders findings deterministically: highest confidence
// first, then by primary evidence ID for stable tie-breaking.
func sortOrphanFindings(findings []foundation.Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		ci := findings[i].Metrics["confidence"]
		cj := findings[j].Metrics["confidence"]
		if ci != cj {
			return ci > cj
		}
		return primaryEvidence(findings[i]) < primaryEvidence(findings[j])
	})
}

// init self-registers the OrphanAnalyzer with the topology registry so
// callers can look it up by name without importing this file directly.
func init() {
	foundation.Register(OrphanAnalyzer{})
}
