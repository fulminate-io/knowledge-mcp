// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// wire_tensions.go holds the client-side tension-universe reads — the charged-node
// universe build and the explicit-human-edge set ReflectTensions pairs on. Split out
// of wire_adjacency.go to keep each file under the 500-line cap; the tension
// predicate is a distinct concern from the clustering adjacency read.

// tensionEdgeTypes is the set of EXPLICIT, SEMANTIC thought↔thought reasoning
// edges that count as a tension link. ReflectTensions pairs two thoughts only
// when an edge of one of these types joins them — bare co-session membership is
// NOT a tension. It is a deliberate per-module duplicate of the server's
// reflection-relevant thought↔thought edge set: the client cannot import the
// server store, exactly like the adjacencyEdgeTypes var in wire_adjacency.go.
// Several edge types are intentionally EXCLUDED:
//
//   - EdgeNext and EdgeBranchesFrom (the TEMPORAL types) are excluded because
//     they carry no propositional content. EdgeNext is auto-created between
//     consecutive same-session thoughts purely by creation order, with zero
//     semantic evaluation, so an opposing-valence plan→blocker arc inside one
//     task's normal progression would otherwise register as a false tension.
//     A temporal sequence is not a disagreement. (Clustering's adjacencyEdgeTypes
//     deliberately KEEPS both temporal types — that set is a separate concern.)
//   - EdgeChargedBy and EdgeEvidencedBy join a thought to a CHARGE, not
//     thought↔thought, so they can never form a tension pair.
//
// "contradicts" has no EdgeType constant (it is a documented mutate(link)
// relationship taken as-given on the wire), so it is keyed as the
// EdgeType("contradicts") string literal.
var tensionEdgeTypes = []kgtypes.EdgeType{
	kgtypes.EdgeRelatesTo,
	kgtypes.EdgeProduced,
	kgtypes.EdgeBecause,
	kgtypes.EdgeSupports,
	kgtypes.EdgeType("contradicts"),
	kgtypes.EdgeInformedBy,
	kgtypes.EdgeSynthesizedFrom,
}

// isMachineTensionMethod reports whether an edge Method tag is one of the four
// MACHINE relates-to writer provenances — i.e. a programmatically densified or
// linked edge, never a human reasoning assertion. It references the EXISTING
// writer consts (treeLinkMethod tree_link.go, densifyMethod similarity_lever.go,
// topicSimilarityMethod similarity.go, artifactLinkMethod artifact_link_write.go)
// rather than re-spelling the string literals, so a writer-const rename breaks
// the build instead of silently slipping a machine edge back into the tension
// set. An empty Method (a human-authored mutate(link)) and any other tag are
// human → false. Used as the edge-slice pre-filter inside fetchTensionEdges:
// machine relates-to edges are clustering signal, not propositional disagreement,
// so they must never pair two thoughts as a tension.
func isMachineTensionMethod(method string) bool {
	switch method {
	case treeLinkMethod, densifyMethod, topicSimilarityMethod, artifactLinkMethod:
		return true
	default:
		return false
	}
}

// tensionClaimTypes is the node-type filter the charged universe is narrowed to —
// the three chargeable claim types tensions has always paired on. Charges attach
// legally to all three (the charge intercept accepts thought, finding and research),
// so the filter is applied to the CHARGE PARENTS, never used to seed a browse.
var tensionClaimTypes = map[kgtypes.NodeType]bool{
	kgtypes.NodeThought:  true,
	kgtypes.NodeFinding:  true,
	kgtypes.NodeResearch: true,
}

// fetchTensionUniverseNodes builds the tension universe by INVERTING it onto the
// charged node set, and returns the per-parent charge map it necessarily built on
// the way.
//
// WHY THE INVERSION IS EXACT. buildTensionCandidates admits a pair only when both
// endpoints have Magnitude >= 0.5, and computePropertiesFromCharges leaves Magnitude
// at exactly 0 unless total charge weight > 0. An UNCHARGED node can therefore never
// qualify, whatever its type — so the charged claim set is a sufficient universe. It
// is thousands of charged-by edges wide instead of tens of thousands of nodes, which
// is why the old three-type full drain (a 3-goroutine fan-out of paged browses, and
// the only 3-way-concurrent match_single_layer issuer in the pass) is gone.
//
// The seed is the CHARGE set, never the thought ids: charges attach legally to
// finding and research too, so a thought-seeded edge walk would silently drop a
// charged finding<->finding tension pair.
//
// Three reads on the cold path, ONE on the warm path:
//  1. the charge set — resident from the corpus cache when the source is warm
//     (ZERO wire calls), else a single type=charge drain;
//  2. ONE bulk EdgeChargedBy read over the charge ids (parent=From, charge=To);
//  3. ONE bulk hydrate of the charge parents, narrowed to tensionClaimTypes.
//
// The returned charge map is in the SAME shape fetchChargesFor produces (joined in
// caller order, parents with no hydratable charge omitted), so ReflectTensions
// consumes it instead of issuing its own full-universe charge read.
func fetchTensionUniverseNodes(ctx context.Context, gc Caller, src CorpusSource) ([]*knowledgev1.Node, map[string][]*knowledgev1.Node, error) {
	if gc == nil {
		return nil, nil, nil
	}
	charges, err := fetchTensionChargeSet(ctx, gc, src)
	if err != nil {
		return nil, nil, err
	}
	if len(charges) == 0 {
		return nil, nil, nil
	}
	chargeByID := make(map[string]*knowledgev1.Node, len(charges))
	chargeIDs := make([]string, 0, len(charges))
	for _, c := range charges {
		if c.GetId() == "" {
			continue
		}
		chargeByID[c.GetId()] = c
		chargeIDs = append(chargeIDs, c.GetId())
	}

	edges, err := fetchEdgesForNodeSet(ctx, gc, chargeIDs, []kgtypes.EdgeType{kgtypes.EdgeChargedBy})
	if err != nil {
		return nil, nil, err
	}
	// EdgeChargedBy is parent(From) -> charge(To): the parents are the From side of
	// every edge whose To is one of our charges.
	parentToChargeIDs := make(map[string][]string, len(chargeIDs))
	parentIDs := make([]string, 0, len(chargeIDs))
	for i := range edges {
		e := &edges[i]
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeChargedBy || chargeByID[e.ToId] == nil {
			continue
		}
		if _, seen := parentToChargeIDs[e.FromId]; !seen {
			parentIDs = append(parentIDs, e.FromId)
		}
		parentToChargeIDs[e.FromId] = append(parentToChargeIDs[e.FromId], e.ToId)
	}
	if len(parentIDs) == 0 {
		return nil, nil, nil
	}

	parentByID := fetchNodesByIDs(ctx, gc, parentIDs)
	universe := make([]*knowledgev1.Node, 0, len(parentIDs))
	chargesByParent := make(map[string][]*knowledgev1.Node, len(parentIDs))
	for _, pid := range parentIDs {
		parent, ok := parentByID[pid]
		if !ok || !tensionClaimTypes[kgtypes.NodeType(parent.GetType())] {
			continue
		}
		parentCharges := make([]*knowledgev1.Node, 0, len(parentToChargeIDs[pid]))
		for _, cid := range parentToChargeIDs[pid] {
			if c, ok := chargeByID[cid]; ok {
				parentCharges = append(parentCharges, c)
			}
		}
		if len(parentCharges) == 0 {
			continue
		}
		universe = append(universe, parent)
		chargesByParent[pid] = parentCharges
	}
	return universe, chargesByParent, nil
}

// fetchTensionChargeSet serves the charge seed from the resident corpus cache when
// the source is warm (the cache already holds charges, so this is an O(1) projection
// with no wire call) and otherwise drains a single type=charge browse — the cold /
// degraded / unit-test path, behavior-equivalent to the pre-cache read.
func fetchTensionChargeSet(ctx context.Context, gc Caller, src CorpusSource) ([]*knowledgev1.Node, error) {
	if cs, ok := src.(ChargeCorpusSource); ok {
		if charges, warm := cs.ChargeSnapshot(); warm {
			return charges, nil
		}
	}
	return drainThoughtBrowse(ctx, gc, string(kgtypes.NodeCharge), browsePageSize)
}

// fetchTensionEdges builds the edge set ReflectTensions pairs on: thoughts joined
// ONLY by an EXPLICIT, HUMAN thought↔thought reasoning edge (tensionEdgeTypes
// minus machine-Method provenances), with NO session-sibling expansion. The
// tension predicate has TWO exclusions, both applied here:
//
//  1. NO session-sibling expansion — fetchAdjacency("all") folds in every
//     co-session pair via deriveSessionSiblings, which made unrelated thoughts
//     sharing a session read as a tension; pairing on explicit edges removes that
//     false adjacency. fetchTensionEdges never reads EdgeKGContains.
//  2. NO machine relates-to edges — every edge whose Method is one of the four
//     machine writer provenances (isMachineTensionMethod: tree-link / topic-densify
//     / topic-similarity / artifact-link) is dropped from the edge slice. Those
//     edges are clustering/densification signal, not propositional disagreement,
//     so a machine link between opposite-valence thoughts is a category error, not
//     a tension.
//
// It returns the in-scope node IDs, the HUMAN-only edge slice (machine relates-to
// edges already removed), the in-scope idSet, and — because the charged-universe
// build already hydrated them — the universe nodeByID map and the per-parent charge
// map. ReflectTensions consumes the edge slice DIRECTLY so it can carry each linking
// edge's Method + Type into the report (the adjacency map alone cannot carry
// Method), and consumes the last two instead of issuing its own hydrate + charge
// read over the whole universe.
//
// Clustering is unaffected: it runs off fetchAdjacency, not this helper, and
// buildAdjacencyFromEdges stays byte-identical — the machine-edge drop is a slice
// pre-filter local to this function, structurally incapable of touching
// cluster-detection adjacency.
//
// Cost is the cheap half of fetchAdjacency("all"): the charged-universe build
// (fetchTensionUniverseNodes — resident charges on a warm pass, so no browse at all)
// + one bulk RETURN_MODE_EDGES read filtered to tensionEdgeTypes
// (fetchEdgesForNodeSet) + a pure client-side O(edges) machine filter. It
// deliberately SKIPS the session-sibling expansion (the extra EdgeKGContains read +
// group-by) that dominates fetchAdjacency("all"). The universe is LOCAL to this
// helper — clustering (fetchAdjacencyNodeIDs) stays on the thought-only
// fetchAllThoughtNodes.
func fetchTensionEdges(ctx context.Context, gc Caller, src CorpusSource) ([]string, []*knowledgev1.Edge, map[string]bool, map[string]*knowledgev1.Node, map[string][]*knowledgev1.Node, error) {
	if gc == nil {
		return nil, nil, nil, nil, nil, nil
	}

	nodes, charges, err := fetchTensionUniverseNodes(ctx, gc, src)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	nodeIDs := make([]string, 0, len(nodes))
	idSet := make(map[string]bool, len(nodes))
	nodeByID := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		nodeIDs = append(nodeIDs, n.Id)
		idSet[n.Id] = true
		nodeByID[n.Id] = n
	}

	edges, err := fetchEdgesForNodeSet(ctx, gc, nodeIDs, tensionEdgeTypes)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	// Drop machine relates-to edges (tree-link / topic-densify / topic-similarity /
	// artifact-link) — they are clustering signal, not tension signal. Collect
	// POINTERS into the read slice (never copy the Edge struct by value — it embeds
	// a protobuf MessageState/sync.Mutex, so a value copy trips copylocks).
	humanEdges := make([]*knowledgev1.Edge, 0, len(edges))
	for i := range edges {
		if isMachineTensionMethod(edges[i].GetMethod()) {
			continue
		}
		humanEdges = append(humanEdges, &edges[i])
	}
	return nodeIDs, humanEdges, idSet, nodeByID, charges, nil
}
