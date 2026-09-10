// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// manifest_narrow.go — THE UPLOAD NARROWING: the diff filter that decides which
// rows go on the wire, and the pass that applies it together with the chunker's
// file grouping while carrying the per-row digests through both. Split out of
// manifest_shadow.go for the repo's file-length cap; nothing else moved.

// filterToChangedRows keeps only the nodes and edges the diff says must go on
// the wire: the changed keys' rows, plus — under a FILE key — every fileless
// node when keepFileless says so.
//
// THE PREDICATE IS THE KEY, and that is the only thing the node key changes
// here. Under diffKeyFile a node is kept when its FILE PATH is in the changed
// set; under diffKeyNode when its OWN ID is. Everything else below — the digest
// alignment, the edges-follow-their-FROM-node rule, the all-or-nothing fileless
// set — is untouched and applies identically.
//
// FILELESS HAS NO PER-FILE DECLINE BASIS. A node belonging to no file — the
// hierarchy package nodes, the repo root, the language hub — is outside the
// manifest by construction, so it is never diffed and nothing there would ever
// mark it changed. keepFileless carries the caller's WHOLE-SET answer instead: it
// is false only when diff mode is ON and the fileless payload's signature matches
// the last DONE-confirmed upload, and true in every other lane.
//
// THE SET IS ALL-OR-NOTHING, and it must be. Uploading fileless NODES without
// their EDGES is the node-present/edge-absent shape that makes the server's
// residual reclaim delete that source's ENTIRE outbound set — every package hub's
// CONTAINS edges. That safety is STRUCTURAL here rather than a convention: edges
// ride on keptIDs[e.FromID], so a dropped fileless node takes its edges with it.
// Do NOT add a second edge predicate.
//
// EDGES FOLLOW THEIR FROM NODE, never their reference site. The owning file of
// an edge is the file_path of its FROM node, so an edge whose source survives
// the filter rides with it; one whose source was filtered out would have landed
// on the wrong file's upload and left its true owner's stale edge uncleared.
// nodeHashes is the per-row digest array index-aligned with nodes, narrowed by
// the SAME predicate and returned alongside the kept nodes. It travels through
// this filter rather than being recomputed after it because the wire contract
// for node_contribution_hashes is index alignment with the chunked node slice —
// re-deriving the surviving subset from a second copy of this predicate is the
// drift this parameter exists to prevent. A nil array narrows to nil, so callers
// with no digests to carry are unaffected.
func filterToChangedRows(
	nodes []*knowledgev1.Node, nodeHashes [][32]byte, edges []kgwire.BatchEdge,
	changed []string, keepFileless bool, kind diffKeyKind,
) (
	[]*knowledgev1.Node, [][32]byte, []kgwire.BatchEdge, error,
) {
	// A MISALIGNED DIGEST ARRAY IS AN ERROR, NEVER A TRUNCATION. Narrowing to
	// whichever array is shorter would hand the chunker digests belonging to other
	// nodes, and the server would then decline files against hashes that were
	// never theirs. Absent (nil) is the one legitimate non-matching length.
	if nodeHashes != nil && len(nodeHashes) != len(nodes) {
		return nil, nil, nil, fmt.Errorf(
			"remote sink: diff filter: %d per-row node digests for %d nodes — the array is index-aligned "+
				"with the node slice by contract, so a differing length means the two came from different passes",
			len(nodeHashes), len(nodes))
	}
	keep := make(map[string]bool, len(changed))
	for _, p := range changed {
		keep[p] = true
	}
	keptIDs := make(map[string]bool, len(nodes))
	// presentIDs is every id THIS RESULT carries, kept or not, and it exists for
	// the orphan-edge rule below. It is built only under the node key, where the
	// question it answers can be asked.
	var presentIDs map[string]bool
	if kind == diffKeyNode {
		presentIDs = make(map[string]bool, len(nodes))
		for _, n := range nodes {
			presentIDs[n.GetId()] = true
		}
	}
	outNodes := make([]*knowledgev1.Node, 0, len(nodes))
	var outHashes [][32]byte
	// Re-slicing to len(nodes) after the equality check above ties the digest
	// array's length to the node loop's bound in the code itself, rather than only
	// in the guard, so indexing it by the node index is locally provable.
	var alignedHashes [][32]byte
	if nodeHashes != nil {
		alignedHashes = nodeHashes[:len(nodes):len(nodes)]
		outHashes = make([][32]byte, 0, len(nodes))
	}
	for i, n := range nodes {
		if !keepNode(n, keep, keepFileless, kind) {
			continue
		}
		keptIDs[n.GetId()] = true
		outNodes = append(outNodes, n)
		if alignedHashes != nil {
			outHashes = append(outHashes, alignedHashes[i])
		}
	}
	outEdges := make([]kgwire.BatchEdge, 0, len(edges))
	for i, e := range edges {
		// AN EDGE THIS FILTER CANNOT PLACE IS AN ERROR, NEVER A SILENT DROP.
		// Placement resolves an edge's owning file through its FROM NODE ID, so an
		// INDEX-ADDRESSED edge — FromIdx/ToIdx pointing into the node slice, with no
		// FromID — has nothing to resolve and would simply vanish here: no error, no
		// log, one lost link. Collector edges are ID-addressed by contract
		// (parser.ToBatchEdges always emits -1/-1 with both IDs, pinned by
		// TestToBatchEdges_AlwaysIDAddressed), so reaching this arm means that
		// contract broke upstream. Dropping information is not an available
		// response to that.
		if e.FromID == "" {
			return nil, nil, nil, fmt.Errorf(
				"remote sink: diff filter: edge %d of %d is INDEX-ADDRESSED (FromIdx=%d, ToIdx=%d, Type=%q, ToID=%q) "+
					"and carries no FromID, so its owning file cannot be resolved and the diff would drop it silently; "+
					"collector edges must be ID-ADDRESSED (FromIdx and ToIdx == -1, with FromID and ToID both set)",
				i+1, len(edges), e.FromIdx, e.ToIdx, e.Type, e.ToID)
		}
		// AN ORPHAN EDGE RIDES EVERY COLLECT — a node-keyed rule with no file-keyed
		// analog, and the one place the node key needs its own always-upload class.
		//
		// Under the FILE key an edge whose FROM node is absent from the result is
		// covered by the fileless group, whose whole-set digest is the client's
		// comparison basis for it. Under the NODE key there is no such group: the
		// owner of an edge IS a node, so an edge whose owner this result never
		// carried belongs to no key, appears in no manifest entry and can never be
		// marked changed. Dropped here it would land once, on the first collect, and
		// never again.
		//
		// IT IS NARROW ON PURPOSE. Only an edge whose FROM node is absent from the
		// WHOLE result qualifies. An edge whose owner IS present and was filtered out
		// as unchanged is correctly dropped — that is the diff working, and its
		// owner's key covers it.
		if keptIDs[e.FromID] || (kind == diffKeyNode && !presentIDs[e.FromID]) {
			outEdges = append(outEdges, e)
		}
	}
	return outNodes, outHashes, outEdges, nil
}

// narrowAndGroupRows applies the diff filter and then the file grouping to a
// collect result in place, returning the per-row node digests that match
// result.Nodes afterwards.
//
// THE TWO STEPS SHARE ONE HELPER BECAUSE THEY ARE ONE OBLIGATION: each transforms
// the node slice — one narrows it, the other permutes it — and the per-row digest
// array must undergo the SAME transformation, because the chunk builder slices
// that array by position. A caller that ran one of them without carrying the
// digests through would store every node under a neighbour's digest, and the
// server's length check cannot see a permutation. Keeping both here means there
// is exactly one place where the pair can drift apart.
//
// THE GROUPING RUNS UNCONDITIONALLY, the filter only under a narrowed decision:
// the chunker packs whole files on every collect, diff or full, so the digests
// must be in file-grouped order on every collect too.
//
// Split out of WriteResult so that function stays inside the package's length
// ceiling, the same reason planDiffUpload and uploadChunks are separate.
func narrowAndGroupRows(
	result *collectorwire.CollectResult, nodeHashes [][32]byte, decision uploadDecision,
) ([][32]byte, error) {
	if !decision.uploadAll {
		keptNodes, keptHashes, keptEdges, fErr := filterToChangedRows(
			result.Nodes, nodeHashes, result.Edges, decision.changed, decision.keepFileless, decision.kind)
		if fErr != nil {
			return nil, fErr
		}
		result.Nodes, result.Edges, nodeHashes = keptNodes, keptEdges, keptHashes
	}
	groupedNodes, groupedHashes, gErr := groupNodesAndHashesByFile(result.Nodes, nodeHashes)
	if gErr != nil {
		return nil, gErr
	}
	result.Nodes = groupedNodes
	return groupedHashes, nil
}

// keepNode is the per-unit node predicate, extracted so the filter's own frame
// carries the DIGEST-ALIGNMENT bookkeeping and nothing else — the two are
// independent obligations and interleaving them is what made the loop hard to
// read.
//
// A NODE-KEYED ROW IS KEPT ON ITS OWN ID AND HAS NO FILELESS ARM. Every node
// carries a key, so there is no undiffable class to wave through and no path to
// consult; falling through to the file predicate instead would read every custom
// node as fileless and either drop the whole graph or upload it whole, depending
// on one boolean.
func keepNode(n *knowledgev1.Node, changed map[string]bool, keepFileless bool, kind diffKeyKind) bool {
	if kind == diffKeyNode {
		return changed[n.GetId()]
	}
	path := n.GetFilePath()
	if path == "" {
		return keepFileless
	}
	return changed[path]
}
