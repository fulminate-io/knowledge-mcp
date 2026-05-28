// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_walk.go implements the BFS path walker that drives the
// public_exposure analyzer family. Starting from each publicSeed, the
// walker explores outgoing edges in the cloud graph until it either runs
// out of edges, hits the max-depth limit, or reaches a sensitive terminal
// (per the shared sensitive classifier in public_exposure_sensitive.go).
//
// The walker is INTENTIONALLY edge-driven — no pair-walk fallback, no
// O(P^2) matrix materialization. It follows the parameterized edge-type
// set it is given, so each analyzer wrapper (aws/k8s/unified) can pick
// exactly which edge types to walk.
//
// STATE KEY. BFS visits are keyed by nodeID (not by (account, id) tuples
// like iam_escalation). Public exposure paths are single-account in the
// common case; the unified variant crosses into the linkage graph via
// proxy IDs that are already globally unique. Using nodeID alone keeps
// the walker simple and correct for the public_exposure use case.
//
// IAM ESCALATION. IAM privilege escalation is NOT modeled as a walkable
// edge. Instead the walker consumes the PERSISTED output of the
// iam_escalation analyzer via iamAdminTerminalReached: when the walker
// arrives at a node, it asks "is this node a known admin-reachable IAM
// role?" and, if so, treats it as a sensitive terminal. This keeps the
// public_exposure analyzer from re-running the expensive iam_escalation
// BFS at every invocation.
//
// DEPTH AND TOP-N. The default max depth is 10 (Request.Extra["max_depth"]
// overrides up to 20). Top-N path cap is 100 (Request.Extra["top_n"]
// overrides). Extra paths past the cap are pruned by minimum score using
// a min-heap on composite score.

import (
	"context"
	"strconv"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

const (
	// defaultMaxExposureDepth is the BFS hop cap. 10 is chosen to cover
	// realistic multi-hop chains (ALB → SG → EC2 → SG → RDS → ...) while
	// still terminating in bounded time on cyclic graphs.
	defaultMaxExposureDepth = 10

	// maxExposureDepthCeiling is the hardest depth any operator-provided
	// override is allowed to set. 20 covers worst-case public-entry
	// chains across cross-VPC peering + cross-graph IRSA bridges.
	maxExposureDepthCeiling = 20

	// defaultExposureTopN is the number of paths retained per analyzer
	// run. The min-heap prunes surplus paths by lowest composite score.
	defaultExposureTopN = 100
)

// walkEdge is one step in a reconstructed attack path. ToID is the
// destination node, Kind is the edge type followed, and Metadata carries
// analyzer-facing context (protocol/port for SG edges, cross_graph=true
// for linkage traversals, etc.).
type walkEdge struct {
	ToID     string
	Kind     kgtypes.EdgeType
	Metadata map[string]string
}

// attackPath is one complete public-entry → sensitive-terminal chain.
// Nodes and Edges are in traversal order: Nodes[0] is the seed, the last
// Node is the terminal, and Edges[i] is the edge taken from Nodes[i] to
// Nodes[i+1].
type attackPath struct {
	Seed            publicSeed
	Nodes           []string
	Edges           []walkEdge
	SensitiveScore  float64
	SensitiveReason string
}

// walkerConfig bundles the per-analyzer-run knobs so bfsFromSeed stays
// short. EdgeTypes is the union of cloud-graph edge types the walker
// follows from each node; empty means "no native edges walked at all".
// FollowLinkageBridge enables the cross-graph linkage lookup for the
// unified analyzer; the AWS and K8s wrappers leave it off.
type walkerConfig struct {
	scoped              *cloudReader
	rootCaller          foundation.GraphCaller
	EdgeTypes           []kgtypes.EdgeType
	MaxDepth            int
	FollowLinkageBridge bool
	// Account is the scoped cloud-graph name — needed when the walker
	// resolves bridge edges back into a per-account context for the
	// finding narrative.
	Account string
}

// walkerParent is the BFS back-pointer entry. Extracted to package scope
// so reconstructExposurePath can take it as a typed argument (an inline
// anonymous struct here would bloat reconstruction signatures).
type walkerParent struct {
	From string
	Edge walkEdge
}

// bfsFromSeed walks outgoing edges from seed.NodeID up to cfg.MaxDepth
// hops, returning every distinct path that ends at a sensitive terminal.
// Cycle detection uses a depth map keyed by nodeID; the first BFS
// discovery of a node wins, so the returned paths are the shortest ones.
//
// This function MUST stay under 80 lines of production code per the
// topology package rule. Helpers live in siblings below.
func bfsFromSeed(ctx context.Context, cfg walkerConfig, seed publicSeed) []attackPath {
	if cfg.scoped == nil || seed.NodeID == "" {
		return nil
	}
	maxDepth := cfg.MaxDepth
	if maxDepth <= 0 || maxDepth > maxExposureDepthCeiling {
		maxDepth = defaultMaxExposureDepth
	}

	parents := map[string]walkerParent{seed.NodeID: {}}
	depth := map[string]int{seed.NodeID: 0}
	queue := []string{seed.NodeID}
	var out []attackPath

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return out
		}
		cur := queue[0]
		queue = queue[1:]
		_, sensitive, score, reason := lookupAndClassify(ctx, cfg, cur)
		if sensitive && cur != seed.NodeID {
			out = append(out, reconstructExposurePath(seed, cur, parents, score, reason))
		}
		if depth[cur] >= maxDepth {
			continue
		}
		for _, e := range walkerOutgoing(ctx, cfg, cur) {
			if e.ToID == "" || e.ToID == cur {
				continue
			}
			if _, seen := depth[e.ToID]; seen {
				continue
			}
			depth[e.ToID] = depth[cur] + 1
			parents[e.ToID] = walkerParent{From: cur, Edge: e}
			queue = append(queue, e.ToID)
		}
	}
	return out
}

// reconstructExposurePath walks the parent map from a sensitive-terminal
// hit back to the seed and returns the complete attack path. parents is
// the BFS back-pointer map. Extracted out of bfsFromSeed so the BFS body
// stays under the 80-line funlen budget.
func reconstructExposurePath(seed publicSeed, terminalID string, parents map[string]walkerParent, score float64, reason string) attackPath {
	var nodes []string
	var edges []walkEdge
	cur := terminalID
	for cur != "" {
		nodes = append([]string{cur}, nodes...)
		if cur == seed.NodeID {
			break
		}
		pi, ok := parents[cur]
		if !ok {
			break
		}
		edges = append([]walkEdge{pi.Edge}, edges...)
		cur = pi.From
		if cur == "" {
			break
		}
	}
	return attackPath{
		Seed:            seed,
		Nodes:           nodes,
		Edges:           edges,
		SensitiveScore:  score,
		SensitiveReason: reason,
	}
}

// lookupAndClassify fetches a node from the scoped reader and asks the
// sensitive classifier whether it is a terminal. Returns the node plus
// the classifier output, so callers can handle both "is it a terminal?"
// and "what's the next-hop set?" in a single wire read.
func lookupAndClassify(ctx context.Context, cfg walkerConfig, nodeID string) (*knowledgev1.Node, bool, float64, string) {
	node, err := cfg.scoped.nodeByID(ctx, nodeID)
	if err != nil || node == nil {
		return nil, false, 0, ""
	}
	sensitive, score, reason := classifySensitive(ctx, cfg.scoped, cfg.rootCaller, node)
	return node, sensitive, score, reason
}

// walkerOutgoing returns every next-hop edge the walker should follow
// from nodeID. It composes native cloud edges (per cfg.EdgeTypes) and,
// when cfg.FollowLinkageBridge is true, cross-graph bridge edges from
// the linkage graph. Returning a unified []walkEdge lets the BFS treat
// both kinds identically.
func walkerOutgoing(ctx context.Context, cfg walkerConfig, nodeID string) []walkEdge {
	edges := outgoingEdgesFromCloud(ctx, cfg.scoped, nodeID, cfg.EdgeTypes)
	if !cfg.FollowLinkageBridge {
		return edges
	}
	bridges := outgoingLinkageBridges(ctx, cfg.rootCaller, cfg.Account, nodeID)
	if len(bridges) > 0 {
		edges = append(edges, bridges...)
	}
	// Also follow the IRSA annotation shortcut directly on the K8s
	// ServiceAccount node, even before the linkage graph is populated.
	// See outgoingIRSAInline for rationale.
	if inline := outgoingIRSAInline(ctx, cfg.scoped, nodeID); len(inline) > 0 {
		edges = append(edges, inline...)
	}
	return edges
}

// outgoingEdgesFromCloud queries the scoped cloud graph for every
// outgoing edge of the parameterized types. The caller (the analyzer
// wrapper) decides which edge types are relevant by passing edgeTypes;
// this helper MUST NOT hardcode any edge type itself — that was OQ4 and
// the layering rule it enforces.
func outgoingEdgesFromCloud(ctx context.Context, scoped *cloudReader, nodeID string, edgeTypes []kgtypes.EdgeType) []walkEdge {
	if scoped == nil || nodeID == "" || len(edgeTypes) == 0 {
		return nil
	}
	edges, _ := scoped.iterEdges(ctx, nodeID, outgoingEdges, edgeTypes)
	var out []walkEdge
	for _, e := range edges {
		we := walkEdge{ToID: e.ToId, Kind: kgtypes.EdgeType(e.Type)}
		if e.Evidence != "" {
			we.Metadata = map[string]string{"evidence": e.Evidence}
		}
		out = append(out, we)
	}
	return out
}

// extractExtraInt reads an integer knob from Request.Extra or returns the
// default. Values outside [1, maxVal] fall through to the default so a
// bad input never silently breaks the walker. The lower bound is fixed
// at 1 — every knob the walker exposes rejects zero/negative values.
func extractExtraInt(extra map[string]string, key string, def, maxVal int) int {
	raw, ok := extra[key]
	if !ok {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || v > maxVal {
		return def
	}
	return v
}
