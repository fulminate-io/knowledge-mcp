// SPDX-License-Identifier: Apache-2.0

package content

// analyzer_structural_motif.go — StructuralMotifAnalyzer groups nodes by
// identical subtree skeleton and emits one Finding per motif. A "skeleton"
// is the deterministic (NodeType, depth) walk of a root's descendants via
// EdgeContains, up to a configurable max depth. Two roots with identical
// skeletons may be two pages following the same pattern template, two
// sections with the same body shape, or two code blocks with the same
// nesting — a general graph-topology pattern, not a web-specific one.
//
// The analyzer is purely graph-type-agnostic: it reads node Type and walks
// outgoing EdgeContains. Page/section/paragraph, file/function/struct, and
// account/vpc/subnet all work without changes. What counts as a "root" is
// configurable via req.Extra["root_types"].
//
// The algorithm is the original pkg/topology body verbatim; only the data
// access swaps from the in-process store (per-node forward-edge + by-id
// queries during the skeleton walk) to the in-memory index built from ONE
// FetchAllNodes + ONE FetchEdges([EdgeContains]) over the whole node set.
//
// Parameters (req.Extra):
//   - root_types: comma-separated node types that participate as motif
//     roots. Default: "section,page".
//   - max_depth: maximum skeleton walk depth (1 = root + immediate
//     children only). Default: 3.
//   - min_members: smallest motif size that surfaces as a Finding. Default: 3.
//
// Severity is percentile-ranked by member count across the motif
// population in this graph (via SeverityFromPercentile).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// motifEvidencePreviewLimit caps how many member node IDs are stored in a
// motif Finding's Evidence slice. Inlined here (value 20) because the original
// shared communityEvidencePreviewLimit lives in the graph family's
// community_analyzer.go, which this disjoint parallel package cannot import.
const motifEvidencePreviewLimit = 20

// StructuralMotifAnalyzer groups graph nodes by identical subtree
// skeleton. Zero-value usable; self-registers via init().
type StructuralMotifAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (StructuralMotifAnalyzer) Name() string { return "structural-motif" }

// Run enumerates root nodes (by type), hashes each root's (NodeType, depth)
// skeleton up to max_depth, groups roots with identical hashes, and emits
// one Finding per group of size >= min_members.
func (a StructuralMotifAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/structural-motif: %w", err)
	}

	rootTypes := parseRootTypes(foundation.ExtraString(req, "root_types", "section,page"))
	maxDepth := foundation.ExtraInt(req, "max_depth", 3, func(v int) bool { return v >= 1 })
	minMembers := foundation.ExtraInt(req, "min_members", 3, func(v int) bool { return v >= 1 })

	nodes, err := foundation.FetchAllNodes(ctx, req.Caller, req.Graph, req.Name)
	if err != nil {
		return nil, fmt.Errorf("topology/structural-motif: fetch nodes %s/%s: %w", req.Graph, req.Name, err)
	}
	idx := buildNodeIndex(nodes)
	// Match-all: idx holds EVERY node of the graph (req.Subset narrows which roots
	// participate later, in collectMotifGroups — never which edges are read), so
	// the contains-edge read wants the whole type-filtered edge set rather than a
	// pivot set that lists every id. buildContainsIndex already ignores an edge
	// whose source is not in idx, so the two reads build the same adjacency.
	edges, err := foundation.FetchAllEdges(ctx, req.Caller, req.Graph, req.Name, []kgtypes.EdgeType{kgtypes.EdgeContains})
	if err != nil {
		return nil, fmt.Errorf("topology/structural-motif: fetch edges %s/%s: %w", req.Graph, req.Name, err)
	}
	adj := buildSortedContainsIndex(edges, idx)

	groups := collectMotifGroups(nodes, idx, adj, rootTypes, maxDepth, req.Subset)
	return buildMotifFindings(idx, req, groups, minMembers), nil
}

// buildSortedContainsIndex builds the parent→children EdgeContains adjacency
// from the bulk edge fetch and sorts each child list by (NodeType, ID), so the
// skeleton walk depends only on child SHAPE, not on the random content-hash of
// child IDs. This is the in-memory equivalent of the original containsChildren
// (forward-edge query then sort by (Type, ID)).
func buildSortedContainsIndex(edges []knowledgev1.Edge, idx nodeIndex) containsChildrenIndex {
	adj := buildContainsIndex(edges, idx)
	for parent := range adj {
		children := adj[parent]
		sort.SliceStable(children, func(i, j int) bool {
			ci, cj := idx[children[i]], idx[children[j]]
			ti, tj := "", ""
			if ci != nil {
				ti = ci.Type
			}
			if cj != nil {
				tj = cj.Type
			}
			if ti != tj {
				return ti < tj
			}
			return children[i] < children[j]
		})
		adj[parent] = children
	}
	return adj
}

// collectMotifGroups computes a skeleton hash for every node whose type is in
// rootTypes and groups roots by identical hash. Subset optionally restricts
// which roots participate (e.g. a closure that skips non-pattern pages).
func collectMotifGroups(
	nodes []*knowledgev1.Node,
	idx nodeIndex,
	adj containsChildrenIndex,
	rootTypes map[kgtypes.NodeType]struct{},
	maxDepth int,
	subset func(*knowledgev1.Node) bool,
) map[string][]string {
	groups := make(map[string][]string)
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if _, ok := rootTypes[kgtypes.NodeType(n.Type)]; !ok {
			continue
		}
		if subset != nil && !subset(n) {
			continue
		}
		hash := skeletonHash(idx, adj, n.Id, maxDepth)
		groups[hash] = append(groups[hash], n.Id)
	}
	return groups
}

// skeletonHash returns a deterministic 16-hex hash of the (NodeType, depth)
// walk rooted at nodeID via EdgeContains, up to maxDepth levels. Children
// are sorted by (Type, ID) so that the hash is order-independent — two pages
// whose sections were stored in different orders still skeletonize
// identically.
func skeletonHash(idx nodeIndex, adj containsChildrenIndex, rootID string, maxDepth int) string {
	var b strings.Builder
	walkSkeleton(idx, adj, rootID, 0, maxDepth, &b)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8]) // 16 hex chars
}

// walkSkeleton emits "(type:depth)" tokens in a deterministic order — children
// pre-sorted by (NodeType, ID) in the adjacency index — to the provided
// builder. Depth 0 is the root itself. Stops recursion once depth == maxDepth.
//
// Sorting by (NodeType, ID) instead of ID alone is the load-bearing detail:
// node IDs are content-hashed, so two siblings of different NodeType inside the
// same parent may sort in different orders across otherwise-identical subtrees.
// Normalizing on NodeType first keeps the skeleton hash stable per shape rather
// than per instance.
func walkSkeleton(idx nodeIndex, adj containsChildrenIndex, nodeID string, depth, maxDepth int, b *strings.Builder) {
	n, ok := idx[nodeID]
	if !ok {
		return
	}
	fmt.Fprintf(b, "(%s:%d)", n.Type, depth)
	if depth >= maxDepth {
		return
	}
	for _, c := range adj[nodeID] {
		walkSkeleton(idx, adj, c, depth+1, maxDepth, b)
	}
}

// buildMotifFindings turns the (hash → member IDs) map into ranked
// Findings. Motifs smaller than minMembers are dropped. Severity is
// percentile-ranked by member count across surviving motifs so that the
// dominant pattern in a graph floats to the top of the severity ladder.
// TopK truncates the ranked slice when req.TopK > 0.
func buildMotifFindings(idx nodeIndex, req foundation.Request, groups map[string][]string, minMembers int) []foundation.Finding {
	items := make([]foundation.ScoredItem, 0, len(groups))
	for hash, members := range groups {
		if len(members) < minMembers {
			continue
		}
		items = append(items, foundation.ScoredItem{ID: hash, Score: float64(len(members))})
	}
	if len(items) == 0 {
		return nil
	}

	allScores := make([]float64, 0, len(items))
	for _, it := range items {
		allScores = append(allScores, it.Score)
	}
	sort.Float64s(allScores)

	k := req.TopK
	if k <= 0 {
		k = len(items)
	}
	top := foundation.TopK(items, k)

	findings := make([]foundation.Finding, 0, len(top))
	for _, it := range top {
		members := groups[it.ID]
		sort.Strings(members)
		findings = append(findings, buildMotifFinding(idx, it.ID, members, allScores))
	}
	return findings
}

// buildMotifFinding constructs one Finding for a motif. Evidence is the
// member-node IDs (the skeleton hash lives in Metadata so callers can
// dedup on a stable hash-keyed primary_evidence when needed) — the first
// member is the primary-evidence node.
func buildMotifFinding(idx nodeIndex, hash string, members []string, allScores []float64) foundation.Finding {
	pct := foundation.Percentile(allScores, float64(len(members)))
	sev := foundation.SeverityFromPercentile(pct)

	preview := members
	if len(preview) > motifEvidencePreviewLimit {
		preview = members[:motifEvidencePreviewLimit]
	}
	evidence := make([]string, len(preview))
	copy(evidence, preview)

	display := resolveNodeName(idx, members[0])

	return foundation.Finding{
		Algorithm: "structural-motif",
		Severity:  sev,
		Title:     fmt.Sprintf("Structural motif %s: %d members", hash, len(members)),
		Summary: fmt.Sprintf(
			"%d nodes share an identical subtree skeleton (hash=%s). "+
				"First member: %s. Top %.1f%% of motifs in this graph by member count.",
			len(members), hash, display, pct,
		),
		Evidence: evidence,
		Metrics: map[string]float64{
			"member_count": float64(len(members)),
			"percentile":   pct,
		},
		Metadata: map[string]string{
			"skeleton_hash": hash,
		},
	}
}

func init() {
	foundation.Register(StructuralMotifAnalyzer{})
}

var _ foundation.Analyzer = StructuralMotifAnalyzer{}
