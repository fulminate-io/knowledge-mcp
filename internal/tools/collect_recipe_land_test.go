// SPDX-License-Identifier: Apache-2.0

// collect_recipe_land_test.go — requirements 1, 2 and 3 of the landing: what a
// landed node carries, the hub it is grouped under, and the edge that is never
// shipped. The fake and its helpers are in collect_recipe_land_harness_test.go,
// which carries this family's reading of why every assertion is against the
// COMPILED PLAN rather than the rendered response.

package tools

import (
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// ---------------------------------------------------------------------------
// Requirement 1 — the landed node's fields, hub key, hub edge and source stamp
// ---------------------------------------------------------------------------

// TestInterceptCollect_Landing_RelaysTheServerValidatorRefusal is the CLIENT half
// of the wrong-kind input class whose server half is
// engine_practice_landing_body_test.go in cmd/knowledge-server.
//
// The class is a body of a KNOWN practice type with no summary, which the mutate
// route's create validator refuses server-side. What this side owns is what the
// landing DOES with that answer: the refusal is relayed to the caller with the
// server's own words intact, and nothing is reported as landed. A landing that
// swallowed it — logged it, or returned the outcome it composed before sending —
// would report a successful landing over a graph that received nothing, and every
// plan-shape cell in this package would still pass.
func TestInterceptCollect_Landing_RelaysTheServerValidatorRefusal(t *testing.T) {
	// The verbatim shape validateSummary emits, so a relay that reformatted or
	// summarized the server's answer is visible here rather than plausible.
	const serverRefusal = `mutate(create): node_bodies[1] (type="pattern", name="Message Router"): ` +
		`summary is required and must be non-empty (search-optimized one-line summary)`

	c := newLandingCaller()
	c.mutateErr = connect.NewError(connect.CodeInvalidArgument, errors.New(serverRefusal))

	deps := &recipeDeps{sink: &recipeCaptureSink{}, gc: c}
	handled, res := InterceptCollect(opCtx(), deps, landParams(t, nil))
	require.True(t, handled)
	require.True(t, res.IsError,
		"a refused batch must reach the caller as an error, never as a landing that reports counts it did not write")

	msg := resultText(res)
	assert.Contains(t, msg, "node_bodies[1]", "the server's batch index survives the relay")
	assert.Contains(t, msg, `type="pattern"`)
	assert.Contains(t, msg, "summary is required", "and its reason, unparaphrased")
	assert.Contains(t, msg, "land", "wrapped in the landing's own context so the caller knows which call failed")

	require.Len(t, c.mutations, 1, "exactly one batch was attempted")
	assert.Empty(t, c.practiceByID,
		"and NOTHING landed: the batch is one atomic create, so a refusal leaves no partial set behind")
}

func TestInterceptCollect_Landing_NodeCarriesEmitFieldsHubKeyAndOneHubEdge(t *testing.T) {
	c := newLandingCaller()
	_, plan := runLand(t, c, nil)

	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, plan.GetKind(),
		"a landing writes through the MUTATE create route, so the server's summary-and-name validator runs")

	router := bodyByName(plan, "Message Router")
	require.NotNil(t, router, "the emitted row must be in the batch")
	assert.Equal(t, "pattern", router.GetType())
	assert.Equal(t, "Message Router", router.GetSummary(), "the emit body's fields ride verbatim")
	assert.Equal(t, "recipe:hohpe-eip", router.GetSource(),
		"the top-level source field keeps the emitter's provenance stamp and is NOT the hub")
	assert.Equal(t, wantHubID(), router.GetMetadata()[kgtypes.MetaKeySourceHub],
		"the hub id rides the dedicated metadata key, which is what the browse and the by-hub delete predicate on")

	// EXACTLY ONE hub edge per landed node, in the node → hub direction. Checking
	// only that the hub appears somewhere on the edge passes under an inversion.
	var hubEdges int
	for _, e := range plan.GetEdges() {
		if e.GetType() != string(kgtypes.EdgeSourcedFrom) {
			continue
		}
		hubEdges++
		assert.Equal(t, wantHubID(), e.GetToId(), "the edge runs TO the hub")
		assert.GreaterOrEqual(t, e.GetFromIdx(), int32(0), "and FROM a body in this batch")
		assert.Equal(t, int32(-1), e.GetToIdx(), "the hub end is addressed by id")
	}
	assert.Equal(t, 2, hubEdges, "one sourced-from per landed row, and the hub itself carries none")
}

// TestInterceptCollect_Landing_ExtractOnlyStillWritesNothing is requirement 1's
// negative half, and it is the control that makes every mutation assertion above
// mean the LAND flag rather than the collect path writing unconditionally.
func TestInterceptCollect_Landing_ExtractOnlyStillWritesNothing(t *testing.T) {
	c := newLandingCaller()
	sink := &recipeCaptureSink{}
	deps := &recipeDeps{sink: sink, gc: c}

	args, err := json.Marshal(map[string]any{
		"type": "web", "id": "hohpe-eip", "transformer": "recipe",
		"recipe_body": landingBody, "extract": true,
	})
	require.NoError(t, err)
	handled, res := InterceptCollect(opCtx(), deps, kgtools.CallToolParams{Name: "collect", Arguments: args})

	require.True(t, handled)
	require.False(t, res.IsError, "expected a successful extract, got: %s", resultText(res))
	assert.Contains(t, resultText(res), "Message Router",
		"THE KNOWN-POSITIVE: the run really did emit, so the zeros below are decisions rather than a no-op")
	assert.Empty(t, c.mutations, "an extract run issues NO mutation")
	assert.Empty(t, sink.results, "and ships nothing to any sink")
	for _, r := range c.reads {
		assert.NotEqual(t, string(kgtypes.GraphPractice), r.graph,
			"an extract run does not read the target graph either: it has no landing to prepare")
	}
}

// ---------------------------------------------------------------------------
// Requirement 2 — the hub, created once and reused
// ---------------------------------------------------------------------------

func TestInterceptCollect_Landing_HubCreatedOnceThenReused(t *testing.T) {
	t.Run("absent hub is created once, named from the slug, with kind and origin", func(t *testing.T) {
		c := newLandingCaller()
		_, plan := runLand(t, c, nil)

		var hubs []*knowledgev1.NodeBody
		for _, b := range plan.GetNodeBodies() {
			if b.GetType() == string(kgtypes.NodeSource) {
				hubs = append(hubs, b)
			}
		}
		require.Len(t, hubs, 1, "exactly ONE hub node is created on a first landing")
		hub := hubs[0]
		assert.Equal(t, wantHubID(), hub.GetId())
		assert.Equal(t, "hohpe-eip", hub.GetName(), "the hub is named from the recipe's source slug")
		assert.NotEmpty(t, hub.GetSummary(), "a source node is never auto-summarized, so the write supplies one")
		assert.Equal(t, "web", hub.GetMetadata()[kgtypes.MetaKeySourceHubKind],
			"the kind marker is the RAW FAMILY the collection came from")
		assert.Equal(t, "www.enterpriseintegrationpatterns.com", hub.GetMetadata()[landingOriginMetaKey],
			"and the origin is the raw graph's own recorded source identity, read off its root")
		assert.Equal(t, wantHubID(), hub.GetMetadata()[kgtypes.MetaKeySourceHub],
			"the hub carries its OWN id under the hub key, which is what makes a by-hub delete sweep the hub with its members")

		// THE PROBE IS ASSERTED BY SELECTOR. A hub probe issued against the wrong
		// graph returns empty and creates a SECOND hub on every run, with no error
		// and no refusal.
		var probes int
		for _, r := range c.reads {
			if r.graph == string(kgtypes.GraphPractice) && r.nodeType == string(kgtypes.NodeSource) {
				probes++
				assert.Empty(t, r.name, "the combined practice graph carries no instance name")
				assert.Empty(t, r.language, "and no language: that selector is refused on a write path")
				assert.Equal(t, wantHubID(), r.meta[kgtypes.MetaKeySourceHub],
					"the probe narrows on the hub key rather than scanning every source node")
			}
		}
		assert.Equal(t, 1, probes, "the hub is probed exactly once per landing")
	})

	t.Run("a resident hub is reused, not duplicated", func(t *testing.T) {
		c := newLandingCaller()
		seedResidentHub(c, wantHubID())
		_, plan := runLand(t, c, nil)

		for _, b := range plan.GetNodeBodies() {
			assert.NotEqual(t, string(kgtypes.NodeSource), b.GetType(),
				"a second landing under a resident hub creates NO hub node")
		}
		require.Len(t, plan.GetNodeBodies(), 2, "only the two emitted rows are created")
		for _, e := range plan.GetEdges() {
			if e.GetType() == string(kgtypes.EdgeSourcedFrom) {
				assert.Equal(t, wantHubID(), e.GetToId(), "and every member still links to the SAME hub")
			}
		}
	})
}

// TestInterceptCollect_Landing_ShipsTheBodysOwnLinkEdges is requirement 1's
// structural half: a recipe body's `link` rules are part of what it emits, and a
// landing that wrote the nodes without them would report success over a graph
// with no structure in it.
//
// THE ENDPOINTS ARE ASSERTED AS BATCH SLOTS, not as ids. A link names EMITTED
// ids, and a collided id lands under a TWIN's id — so an edge copied verbatim
// would point at the resident the twin was versioned away from rather than at the
// node this landing wrote.
func TestInterceptCollect_Landing_ShipsTheBodysOwnLinkEdges(t *testing.T) {
	c := newLandingCaller()
	c.sourceNodes = append(landingSourceRows(), &knowledgev1.Node{
		Id: "s3", Type: "section", SymbolName: "Message Endpoint",
	})
	body := "select section\n" +
		"emit pattern {\n    type := \"pattern\"\n    name := section.symbol_name\n    summary := section.symbol_name\n} as $p\n" +
		"lookup pattern by section.symbol_name as $q\n" +
		"link $p --[relates-to]--> $q"
	_, plan := runLand(t, c, map[string]any{"recipe_body": body})

	var links int
	for _, e := range plan.GetEdges() {
		if e.GetType() != "relates-to" {
			continue
		}
		links++
		assert.GreaterOrEqual(t, e.GetFromIdx(), int32(0),
			"a link endpoint is a SLOT in this batch, so it follows the node this landing actually wrote")
		assert.GreaterOrEqual(t, e.GetToIdx(), int32(0))
		assert.Empty(t, e.GetFromId(), "and never a raw id copied out of the run")
		assert.Empty(t, e.GetToId())
	}
	assert.Positive(t, links, "the body's link rules reach the batch rather than being dropped")

	// THE CONTROL: the same body with no link rule ships no relates-to edge, so
	// the count above is the rule firing rather than an edge stamped anyway.
	plain := newLandingCaller()
	_, plainPlan := runLand(t, plain, nil)
	for _, e := range plainPlan.GetEdges() {
		assert.NotEqual(t, "relates-to", e.GetType(),
			"a body with no link rule produces no structural edge")
	}
}

// ---------------------------------------------------------------------------
// Requirement 3 — no edge into the raw graph, of any type
// ---------------------------------------------------------------------------

func TestInterceptCollect_Landing_ShipsNoRawGraphEdge(t *testing.T) {
	c := newLandingCaller()
	_, plan := runLand(t, c, nil)

	rawIDs := map[string]bool{"root": true, "s1": true, "s2": true}
	require.NotEmpty(t, plan.GetEdges(),
		"THE KNOWN-POSITIVE: the batch really does carry edges, so the assertions below are not vacuous")
	for _, e := range plan.GetEdges() {
		assert.NotEqual(t, string(kgtypes.EdgeTranslatedFrom), e.GetType(),
			"no translated-from edge is shipped: the emitter no longer builds one")
		assert.False(t, rawIDs[e.GetToId()],
			"and no edge of ANY type points INTO the raw source graph")
		assert.False(t, rawIDs[e.GetFromId()],
			"nor out of it")
	}
}

// TestInterceptCollect_Landing_RefusesAnEdgeReachingOutsideTheEmittedSet is the
// OTHER half of requirement 3, and it is the half a shipped-edge filter would
// have hidden.
//
// The landing re-points the run's own link edges onto batch slots. An edge whose
// endpoint this landing did not emit — the retired provenance edge into the raw
// graph being exactly that shape — can be neither written (it addresses a graph
// this landing does not own) nor dropped (that discards a value the run
// produced), so the run is refused naming the edge. Without this arm, restoring
// the provenance edge upstream would land the nodes and quietly lose it.
func TestInterceptCollect_Landing_RefusesAnEdgeReachingOutsideTheEmittedSet(t *testing.T) {
	// DRIVEN AT composeLanding, the production symbol that places the edges,
	// because the offending edge cannot be produced through the collect surface:
	// the emitter that used to build one was removed by this same change. That is
	// the point of the cell — it is the observer standing where the retired
	// producer was, so restoring one upstream reaches this refusal rather than
	// landing the nodes and quietly losing the edge.
	pre := &landingPreflight{hubID: "hub-1", kind: "web", origin: "example.org", slug: "hohpe-eip"}
	emitted := recipe.StableID(landingTargetKey(), "hohpe-eip", "pattern", "Message Router")
	nodes := []*knowledgev1.Node{{Id: emitted, Type: "pattern", SymbolName: "Message Router", Summary: "s"}}

	_, _, err := composeLanding(pre, nodes, []kgwire.BatchEdge{{
		FromIdx: -1, ToIdx: -1, FromID: emitted, ToID: "s1", // "s1" is a RAW source row
		Type: kgtypes.EdgeTranslatedFrom,
	}}, map[string]landingResidency{})

	require.Error(t, err, "an edge reaching outside the emitted set must refuse the landing")
	assert.Contains(t, err.Error(), "s1", "the refusal names the endpoint it could not place")
	assert.Contains(t, err.Error(), "translated-from", "and the edge type it arrived under")
	assert.Contains(t, err.Error(), "Nothing was written")

	// THE CONTROL, same call shape: an edge BETWEEN two emitted nodes is placed
	// rather than refused, so the refusal above is the endpoint being outside the
	// set rather than link edges being rejected outright.
	second := recipe.StableID(landingTargetKey(), "hohpe-eip", "pattern", "Message Channel")
	both := append(nodes, &knowledgev1.Node{Id: second, Type: "pattern", SymbolName: "Message Channel", Summary: "s"})
	args, _, cerr := composeLanding(pre, both, []kgwire.BatchEdge{{
		FromIdx: -1, ToIdx: -1, FromID: emitted, ToID: second, Type: "relates-to",
	}}, map[string]landingResidency{})
	require.NoError(t, cerr)
	var relates int
	for _, e := range args.Edges {
		if e.Type == "relates-to" {
			relates++
		}
	}
	assert.Equal(t, 1, relates, "an in-set link edge is placed onto the batch")
}
