// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestBuildAdjacency_ExcludesCodeRefProxyEdges is the structural guard that a
// born-link edge (thought--relates-to-->code-proxy, Method="code-ref")
// can never enter the clustering adjacency and mint a spurious proxy hub. The
// scope="all" idSet is built EXCLUSIVELY from thought nodes (fetchAllThoughtNodes
// drains no proxies), and buildAdjacencyFromEdges admits an edge only when BOTH
// endpoints are in the idSet — so the proxy ToID, absent from the thought-only
// idSet, drops the born-link edge. This binds to the SAME production projection
// (buildAdjacencyFromEdges); a re-implemented copy of the drop logic would be
// worthless.
//
// Distinct from the origin-agent exclusion guard in wire_adjacency_origin_test.go
// — that guards the agent/skill origin hubs via the keepInAllTypesIDSet
// predicate; this guards the code-ref proxy via the both-endpoints filter on a
// thought-only idSet.
func TestBuildAdjacency_ExcludesCodeRefProxyEdges(t *testing.T) {
	// Thought-only idSet (what scope="all" fetchAllThoughtNodes yields — proxies
	// are never drained in). Two thoughts: one cites a code referent, plus a
	// control sibling.
	idSet := map[string]bool{"th-1": true, "th-2": true}

	// The born-link proxy id is NOT in the idSet (proxies never are).
	const proxyID = "proxy:knowledge:tools/wire.go:PersistBatch"

	edges := []knowledgev1.Edge{
		// Born-link: thought--relates-to-->code-proxy (Method code-ref). Must drop —
		// the proxy endpoint is out of the thought-only idSet.
		{FromId: "th-1", ToId: proxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref"},
		// Positive control: thought<->thought relates-to. Must survive.
		{FromId: "th-1", ToId: "th-2", Type: string(kgtypes.EdgeRelatesTo)},
	}
	adj := buildAdjacencyFromEdges(edges, idSet, adjacencyEdgeTypes)

	// The code-ref proxy is excluded entirely — never an adjacency key, never a
	// neighbor of the citing thought.
	assert.NotContains(t, adj, proxyID,
		"the out-of-idSet code proxy must not appear as an adjacency key")
	assert.NotContains(t, adj["th-1"], proxyID,
		"th-1 must not gain the code proxy as a neighbor (born-link edge dropped structurally)")

	// Positive control: the thought<->thought relates-to edge survives in both
	// directions, proving the exclusion is specific to the out-of-idSet proxy
	// endpoint, not a blanket drop of th-1's neighbors.
	assert.Contains(t, adj["th-1"], "th-2", "the thought<->thought edge must survive")
	assert.Contains(t, adj["th-2"], "th-1", "the thought<->thought edge is bidirectional")
}

// TestBuildAdjacency_TypeFilterExcludesInSetNonAdjacencyEdge is the ONLY catcher for
// an inert type filter, and the shape is the whole point.
//
// Every other buildAdjacencyFromEdges test drops its unwanted edge because ONE
// ENDPOINT IS OUTSIDE idSet — a code proxy, an agent hub. Under those fixtures a type
// filter that did nothing at all would still pass. Only an edge with BOTH endpoints
// INSIDE idSet and a type OUTSIDE keepTypes can tell a working filter from an absent
// one, and that is exactly the case the unified 7-type read newly makes reachable:
// before the collapse, no kg-contains or charged-by edge ever arrived here.
//
// The two thoughts joined by a kg-contains edge are contrived on purpose — the point
// is the predicate, not the plausibility of the fixture.
func TestBuildAdjacency_TypeFilterExcludesInSetNonAdjacencyEdge(t *testing.T) {
	idSet := map[string]bool{"th-1": true, "th-2": true}

	// Constructed in place on every use rather than copied from named values:
	// knowledgev1.Edge embeds a protoimpl.MessageState containing a sync.Mutex, so
	// copying one trips go vet's copylocks check.
	nonAdjacencyOnly := func() []knowledgev1.Edge {
		return []knowledgev1.Edge{
			{FromId: "th-1", ToId: "th-2", Type: string(kgtypes.EdgeKGContains)},
		}
	}

	// FILTERED: the non-adjacency edge is excluded despite both endpoints being in
	// the idSet; the relates-to control survives, so the exclusion is the TYPE and
	// not the idSet.
	filtered := buildAdjacencyFromEdges([]knowledgev1.Edge{
		{FromId: "th-1", ToId: "th-2", Type: string(kgtypes.EdgeKGContains)},
		{FromId: "th-1", ToId: "th-2", Type: string(kgtypes.EdgeRelatesTo)},
	}, idSet, adjacencyEdgeTypes)
	assert.Equal(t, []string{"th-2"}, filtered["th-1"],
		"only the relates-to neighbor survives — the in-idSet kg-contains edge is "+
			"excluded BY TYPE, which no other fixture in this package can detect")

	// ONLY the non-adjacency edge: the map must be empty, not merely smaller.
	only := buildAdjacencyFromEdges(nonAdjacencyOnly(), idSet, adjacencyEdgeTypes)
	assert.Empty(t, only,
		"an edge whose type is outside keepTypes contributes nothing at all")

	// NIL keepTypes = keep every type. This pins the all_types contract in the same
	// place as the filter, so a later "tightening" of the nil case fails here.
	unfiltered := buildAdjacencyFromEdges(nonAdjacencyOnly(), idSet, nil)
	assert.Equal(t, []string{"th-2"}, unfiltered["th-1"],
		"nil keepTypes keeps EVERY type — the same edge survives")
}
