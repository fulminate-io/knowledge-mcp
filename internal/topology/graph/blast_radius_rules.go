// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// blast_radius_rules.go declares the redundancy registry consumed by the
// BlastRadiusAnalyzer (see blast_radius.go). The analyzer walks the reverse
// dependency tree from a seed node and, for every intermediate node, asks
// "how redundant is this hop?" — a load balancer fronting six target groups
// is far more resilient to a single dependency failure than a load balancer
// fronting one target group, and the blast score should reflect that.
//
// LAYOUT — each rule is a small function keyed against a "resource_type"
// metadata value, registered via init() into a package-level dispatch map.
// Adding a new resource type is one function plus one register call.
//
// CONTRACT — every rule returns a redundancy *factor* in the closed range
// [minRedundancyFactor, 1.0]. A factor of 1.0 means "no redundancy" (the
// dependency is fully load-bearing). Factors smaller than 1.0 dampen the
// blast contribution proportionally. The minimum is bounded away from zero
// so a single misconfigured rule can never silently mute the analyzer's
// output. NaN, Inf, or non-positive returns from a rule are clamped up to
// minRedundancyFactor — never to 0 — to preserve the criterion that the
// analyzer always produces a meaningful score for every reachable node.
//
// The edge-count reads go through one bulk foundation.FetchEdges per node
// filtered to the relevant edge type, then count the matching direction in
// memory — the wire twin of the prior per-node IterEdges count.

// minRedundancyFactor is the floor applied to every redundancy weight. We
// never let a node's weight drop below this so a single rule bug or extreme
// metadata value can never zero out the blast score. 0.05 = "at most 20×
// dampening" which is more than enough headroom for the v1 rules below.
const minRedundancyFactor = 0.05

// defaultRedundancyFactor is the weight returned for any node that has no
// rule registered for its resource_type. The analyzer treats every node
// as fully load-bearing by default — only nodes with explicit redundancy
// metadata get a discount.
const defaultRedundancyFactor = 1.0

// RedundancyRule decides how redundancy-weighted a single intermediate node
// is when computing blast radius. Rules return a multiplicative factor in
// [minRedundancyFactor, 1.0] — see the package comment for the contract.
//
// Parameters:
//   - ctx  — request context. Rules MUST honor cancellation.
//   - req  — the foundation.Request the analyzer is walking; rules read
//     replica counts, target counts, and similar redundancy signals
//     over the wire through req.Caller / req.Graph / req.Name.
//   - node — the candidate intermediate node being weighted.
//
// Return:
//   - factor — the multiplicative weight in [minRedundancyFactor, 1.0]. The
//     analyzer applies this directly to the per-hop blast contribution.
//   - err    — non-nil only on wire/query failure. The BFS aborts on the
//     first non-nil error to match the rest of the topology error policy.
type RedundancyRule func(ctx context.Context, req foundation.Request, node *knowledgev1.Node) (factor float64, err error)

// redundancyRules is the package-level dispatch table. Each rule registers
// itself at init() time via registerRedundancyRule. The map is read-mostly
// after init() so we guard with an RWMutex.
var (
	redundancyRulesMu sync.RWMutex
	redundancyRules   = map[string]RedundancyRule{}
)

// registerRedundancyRule adds a rule for a given resource_type to the
// dispatch table. Panics on nil rule, empty resourceType, or duplicate
// registration — all programmer errors that should never reach a running
// server.
func registerRedundancyRule(resourceType string, rule RedundancyRule) {
	if rule == nil {
		panic("topology: registerRedundancyRule called with nil RedundancyRule")
	}
	if resourceType == "" {
		panic("topology: registerRedundancyRule called with empty resourceType")
	}
	redundancyRulesMu.Lock()
	defer redundancyRulesMu.Unlock()
	if _, dup := redundancyRules[resourceType]; dup {
		panic(fmt.Sprintf("topology: duplicate redundancy rule registration: %q", resourceType))
	}
	redundancyRules[resourceType] = rule
}

// lookupRedundancyRule returns the rule registered for the given
// resource_type and a boolean indicating whether a rule was found.
func lookupRedundancyRule(resourceType string) (RedundancyRule, bool) {
	redundancyRulesMu.RLock()
	defer redundancyRulesMu.RUnlock()
	r, ok := redundancyRules[resourceType]
	return r, ok
}

// redundancyFactor is the safe entry point used by the BFS in
// blast_radius_bfs.go. It looks up the rule, invokes it, clamps the
// returned value into [minRedundancyFactor, 1.0], and substitutes
// defaultRedundancyFactor for nodes whose resource_type has no registered
// rule. Returns a non-nil error only on rule-side failures so the BFS can
// abort cleanly.
func redundancyFactor(ctx context.Context, req foundation.Request, node *knowledgev1.Node) (float64, error) {
	resourceType := metaValue(node, "resource_type")
	if resourceType == "" {
		return defaultRedundancyFactor, nil
	}
	rule, ok := lookupRedundancyRule(resourceType)
	if !ok {
		return defaultRedundancyFactor, nil
	}
	f, err := rule(ctx, req, node)
	if err != nil {
		return defaultRedundancyFactor, err
	}
	return clampFactor(f), nil
}

// clampFactor enforces the [minRedundancyFactor, 1.0] envelope on a raw
// rule output. NaN and negative values are clamped up to the floor; values
// above 1.0 are clamped down to 1.0. Centralizing this guarantees the
// criterion that the analyzer never multiplies a per-hop contribution by
// zero (silently muting findings) or by a number greater than one
// (artificially inflating findings).
func clampFactor(f float64) float64 {
	// NaN check: NaN != NaN.
	if f != f {
		return minRedundancyFactor
	}
	if f < minRedundancyFactor {
		return minRedundancyFactor
	}
	if f > 1.0 {
		return 1.0
	}
	return f
}

// replicaRedundancyRule reads the "replicas" metadata field and returns
// 1 / max(replicas, 1). Used by Deployment and StatefulSet workloads.
// Missing, malformed, or zero replicas → defaultRedundancyFactor (1.0).
// Parse failures are deliberately swallowed: a node with garbage in its
// replicas field is a collector/upstream issue, not a reason to abort the
// blast radius BFS. The default factor keeps the analyzer informative
// even against partially-broken metadata.
func replicaRedundancyRule(_ context.Context, _ foundation.Request, node *knowledgev1.Node) (float64, error) {
	raw := metaValue(node, "replicas")
	if raw == "" {
		return defaultRedundancyFactor, nil
	}
	n, parseErr := strconv.Atoi(raw)
	if parseErr != nil {
		// Malformed metadata: fall back to the default rather than
		// fail the entire BFS for one bad node.
		return defaultRedundancyFactor, nil //nolint:nilerr // intentional: parse failure is non-fatal
	}
	if n <= 1 {
		return defaultRedundancyFactor, nil
	}
	return 1.0 / float64(n), nil
}

// outgoingTargetsRedundancyRule counts outgoing TARGETS edges (load balancer
// → target group / backend) and returns 1 / max(count, 1). Used by ELBv2
// load balancers and GCP backend services — both fan traffic across multiple
// downstream targets via the same edge type.
func outgoingTargetsRedundancyRule(ctx context.Context, req foundation.Request, node *knowledgev1.Node) (float64, error) {
	count, err := countEdges(ctx, req, node.Id, kgtypes.EdgeTargets, "out")
	if err != nil {
		return defaultRedundancyFactor, err
	}
	if count <= 1 {
		return defaultRedundancyFactor, nil
	}
	return 1.0 / float64(count), nil
}

// incomingTargetsRedundancyRule counts incoming TARGETS edges (target group
// ← load balancers) and returns 1 / max(count, 1). A target group fronted
// by N load balancers is N-way redundant with respect to the LB layer.
func incomingTargetsRedundancyRule(ctx context.Context, req foundation.Request, node *knowledgev1.Node) (float64, error) {
	count, err := countEdges(ctx, req, node.Id, kgtypes.EdgeTargets, "in")
	if err != nil {
		return defaultRedundancyFactor, err
	}
	if count <= 1 {
		return defaultRedundancyFactor, nil
	}
	return 1.0 / float64(count), nil
}

// countEdges returns the number of edges of the given type incident to
// nodeID in the requested direction ("out" → edges whose FromId is nodeID,
// "in" → edges whose ToId is nodeID). One bulk FetchEdges over the single
// node filtered to edgeType, counted in memory — the wire twin of the prior
// per-node IterEdges count.
func countEdges(ctx context.Context, req foundation.Request, nodeID string, edgeType kgtypes.EdgeType, direction string) (int, error) {
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, []string{nodeID}, []kgtypes.EdgeType{edgeType})
	if err != nil {
		return 0, fmt.Errorf("topology/blast_radius: count edges on %s: %w", nodeID, err)
	}
	n := 0
	for i := range edges {
		e := &edges[i]
		switch direction {
		case "out":
			if e.FromId == nodeID {
				n++
			}
		case "in":
			if e.ToId == nodeID {
				n++
			}
		}
	}
	return n, nil
}

// init registers the v1 redundancy rules. Resource type strings match the
// values emitted by the cloud collectors (k8s, aws, gcp).
func init() {
	registerRedundancyRule("Deployment", replicaRedundancyRule)
	registerRedundancyRule("StatefulSet", replicaRedundancyRule)
	registerRedundancyRule("elbv2-loadbalancer", outgoingTargetsRedundancyRule)
	registerRedundancyRule("elbv2-targetgroup", incomingTargetsRedundancyRule)
	registerRedundancyRule("gcp:compute:backendService", outgoingTargetsRedundancyRule)
}
