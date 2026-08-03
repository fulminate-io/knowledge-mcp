// SPDX-License-Identifier: Apache-2.0

package bootstrap

// query_dispatch_parity_test.go is the READ-side dispatch parity harness: it
// declares a disposition for every published query mode and every documented
// shape combination, then drives each declared cell through the REAL intercept
// chain and asserts the observation matches the declaration.
//
// It lives in package bootstrap because the generic deny is emitted by
// engine.Dispatch AFTER the whole chain declines, so "no published mode reaches
// the generic deny" is only observable end-to-end. The per-arm behavior tests
// stay in package tools where the fakes are precise; this asserts the
// end-to-end SELECTION invariant only — which arm out of the ordered chain
// actually answers a given shape.
//
// There is deliberately NO runtime read-side gate to mirror the write side's.
// A terminal gate would have to whitelist every legitimate fall-through to the
// engine and would become a new source of FALSE REJECTIONS of working shapes.
// The declaration is test-only; drift is prevented by driving the real chain.
//
// SCOPE: graph "", graph "knowledge", and REGISTERED CUSTOM graphs. The custom
// cells were added once the custom-graph arm became a twin of the knowledge arm
// — a shared post-hydrate tail and a mirrored claim gate — because the property
// worth pinning is that the two arms answer equivalent payloads equivalently,
// and that is only observable with both in one harness. Still excluded, each
// having its own claim arm and its own suites: cloud/cicd, practice/linkage,
// and code.
//
// TWO BLIND SPOTS, so a green run is not over-read:
//
//	(a) MARKER PRECISION. A disposition is observed through marker substrings in
//	    the rendered result, so it proves which arm answered only as precisely as
//	    those markers are unique. A cell with no marker proves only that SOMETHING
//	    claimed the call and it was not the generic deny.
//	(b) CLAIMED, NOT WHICH-ARM. A cell declared "claimed by some named arm" and
//	    observed as "not the generic deny" cannot distinguish one claiming arm
//	    from another. Only the cells carrying an arm-specific marker do that.
//
// A third asymmetry, NARROWED once the per-arm param accounting gate went live.
// It used to cover the two deliberate-ignore rows as well: the knowledge search
// arm returned its degrade marker before any post-filter ran, so the harness
// could not see that status or id went unapplied. Those two rows are now
// shapeRejectedByAccounting and ARE observable here — the gate refuses ahead of
// the degrade path and names the param. What remains asymmetric is the recall
// row, still observable only as ARM SELECTION. Distinguishing consumed from
// ignored on an arm that ACCEPTS a param still needs a named behavior test with
// a live fake, which is what the per-arm suites are for.
//
// Not parallel: the counting engine accumulates call state on unsynchronised
// fields, the same construction constraint the write-side harness documents.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

const (
	// genericDenyMarker is the literal fragment engine.Dispatch emits when the
	// whole chain declined and Compile did not recognize the shape. No published
	// mode may reach it — that is this harness's headline invariant.
	genericDenyMarker = "not a recognized engine-reducible shape"
	// knowledgeSearchArmMarker is unique to the client knowledge search arm and
	// is returned BEFORE any Execute RPC when the segment engine is unwired,
	// which is exactly the fixture state here. It is how a cell is observed to
	// have landed on that arm rather than merely "not denied".
	knowledgeSearchArmMarker = "knowledge search: client segment engine unavailable"
	// refusedByIDSelectorMarker is the locked by-id refusal phrase.
	refusedByIDSelectorMarker = "is not applied by a by-id read"
	// registeredGraphSearchArmMarker is the custom-graph twin of
	// knowledgeSearchArmMarker: composeRegisteredGraphSearch returns it BEFORE any
	// Execute RPC when the segment Manager is unwired, which is this fixture's
	// state. It cannot collide with the knowledge marker — that one is prefixed
	// with the graph type, and no custom graph is named "knowledge". "hellograph"
	// is the same fictional graph the package tools suite uses.
	registeredGraphSearchArmMarker = "hellograph search: client segment engine unavailable"
)

// parityDriveResult is one observed chain+dispatch outcome.
type parityDriveResult struct {
	handled   bool
	body      string
	execDelta int32
}

// driveQueryParity composes exactly what production composes: run the intercept
// chain, and when it declines, fall through to engineDispatch. Returns the
// observation the declared rules are checked against.
func driveQueryParity(t *testing.T, c *client, eng *countingEngine, args map[string]any) parityDriveResult {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)

	before := eng.execute.Load()
	ctx := opCtx()
	_, handled, res := c.runInterceptChain(ctx, kgtools.CallToolParams{Name: "query", Arguments: raw})
	if !handled {
		res, err = c.engineDispatch(ctx, "query", raw)
		require.NoError(t, err, "engineDispatch must render its outcome, not return a Go error")
	}
	body := ""
	if len(res.Content) > 0 {
		body = res.Content[0].Text
	}
	return parityDriveResult{handled: handled, body: body, execDelta: eng.execute.Load() - before}
}

// newParityClient builds the chain-drive fixture. The pipeline flag is set so
// the knowledge search arm leaves the wiring-window path and reaches its
// unwired-segment degrade marker; the worker and propagation flags are left
// false, which keeps those arms on their loud not-ready message — a CLAIM, not
// a deny, so the invariant still holds for them.
func newParityClient(t *testing.T) (*client, *countingEngine) {
	t.Helper()
	localURL, eng := startCountingEngine(t)
	c := buildE2EClient(graphclient.NewGraphClientForURL(localURL), "http://cloud.invalid", newFakeAuthStore(), 0)
	c.markPipelineReady()
	return c, eng
}

// TestQueryDispatchParity_EveryPublishedModeIsClassified is the no-skip guard.
// The harness could otherwise pass vacuously by driving a subset, so the live
// enum is read from the schema and compared against the declaration in both
// directions.
func TestQueryDispatchParity_EveryPublishedModeIsClassified(t *testing.T) {
	modeProp, ok := tools.QueryToolDef().InputSchema.Properties["mode"]
	require.True(t, ok, "the query schema must declare a mode property")
	require.NotEmpty(t, modeProp.Enum, "the mode property must publish an enum")

	declared := map[string]int{}
	for _, d := range queryModeDispositions {
		declared[d.mode]++
	}
	for _, mode := range modeProp.Enum {
		assert.Equalf(t, 1, declared[mode],
			"published mode %q must have EXACTLY ONE declared disposition (found %d)", mode, declared[mode])
	}
	published := map[string]bool{}
	for _, mode := range modeProp.Enum {
		published[mode] = true
	}
	for _, d := range queryModeDispositions {
		assert.Truef(t, published[d.mode], "declared mode %q is absent from the published enum — stale entry", d.mode)
	}
	assert.Lenf(t, queryModeDispositions, len(modeProp.Enum),
		"the declaration and the live enum must name the same mode set")

	seen := map[string]bool{}
	for _, s := range queryShapeDispositions {
		assert.Falsef(t, seen[s.name], "shape row %q is declared twice", s.name)
		seen[s.name] = true
		if s.deliberate {
			assert.NotEmptyf(t, s.justification,
				"shape %q is declared a deliberate ignore with no justification — state the mechanism "+
					"that makes ignoring it correct, or fix it", s.name)
		}
		if s.kind == shapeRejectedByAccounting {
			assert.NotEmptyf(t, s.marker,
				"shape %q is declared an accounting rejection with no marker — name the param the "+
					"refusal must mention, else the row proves only that something errored", s.name)
			assert.NotEmptyf(t, s.justification,
				"shape %q is declared an accounting rejection with no justification — record what the "+
					"arm used to do with the param, so the transition stays legible", s.name)
		}
	}
}

// TestQueryDispatchParity_NoPublishedModeReachesTheGenericDeny drives every
// published mode through the real chain. The headline invariant is the one the
// ticket exists for: a mode the schema advertises must never be answered by the
// generic engine deny.
func TestQueryDispatchParity_NoPublishedModeReachesTheGenericDeny(t *testing.T) {
	for _, d := range queryModeDispositions {
		t.Run(d.mode, func(t *testing.T) {
			c, eng := newParityClient(t)
			got := driveQueryParity(t, c, eng, d.args)

			assert.NotContainsf(t, got.body, genericDenyMarker,
				"mode %q reached the GENERIC DENY — it is advertised in the schema and claimed by "+
					"nobody. Observed: %s", d.mode, got.body)

			switch d.kind {
			case dispositionEngineReducible:
				assert.Falsef(t, got.handled, "mode %q must decline to the engine", d.mode)
			case dispositionClaimed, dispositionStructuredRejection:
				assert.Truef(t, got.handled, "mode %q must be CLAIMED by a client arm. Observed: %s",
					d.mode, got.body)
			}
			if d.marker != "" {
				assert.Containsf(t, got.body, d.marker,
					"mode %q must carry its arm-identifying marker. Observed: %s", d.mode, got.body)
			}
		})
	}
}

// TestQueryDispatchParity_DefaultModeShapeGrid drives every declared shape. Two
// row kinds exist ONLY here because they are unobservable anywhere else:
// shapeRefusedByPrecheck (the intercept correctly declines and the refusal
// happens downstream in the engine) and shapeRecallArm (an engine-level test has
// no chain, so it cannot see which arm claimed first).
func TestQueryDispatchParity_DefaultModeShapeGrid(t *testing.T) {
	for _, s := range queryShapeDispositions {
		t.Run(s.name, func(t *testing.T) {
			c, eng := newParityClient(t)
			got := driveQueryParity(t, c, eng, s.args)

			switch s.kind {
			case shapeKnowledgeSearchArm:
				assert.Truef(t, got.handled, "%s must be claimed by the knowledge search arm", s.name)
				assert.Containsf(t, got.body, knowledgeSearchArmMarker,
					"%s must land on the knowledge search arm. Observed: %s", s.name, got.body)
				assert.Zerof(t, got.execDelta, "%s must not reach an Execute RPC", s.name)
			case shapeEngineRead:
				assert.Falsef(t, got.handled, "%s must decline to the engine", s.name)
				assert.NotContainsf(t, got.body, genericDenyMarker, "%s must be served, not denied", s.name)
				assert.Equalf(t, int32(1), got.execDelta, "%s must reach exactly one Execute RPC", s.name)
			case shapeRefusedByPrecheck:
				assert.Falsef(t, got.handled, "%s: the chain declines and the ENGINE refuses", s.name)
				assert.Containsf(t, got.body, s.marker,
					"%s must carry the by-id refusal. Observed: %s", s.name, got.body)
				// The load-bearing half: the refusal happened pre-Compile, not
				// after a read already ran.
				assert.Zerof(t, got.execDelta, "%s must be refused BEFORE any Execute RPC", s.name)
			case shapeRecallArm:
				// Deliberately weak: it pins that the recall arm claimed the call
				// ahead of the engine, which is the whole point of the row, without
				// asserting what recall renders against an unwired runtime.
				assert.Truef(t, got.handled, "%s must be claimed by the recall arm", s.name)
				assert.NotContainsf(t, got.body, genericDenyMarker, "%s must not be denied", s.name)
				assert.NotContainsf(t, got.body, knowledgeSearchArmMarker,
					"%s must be claimed by recall, NOT the knowledge search arm", s.name)
			case shapeRejectedByAccounting:
				// The claiming arm refuses the payload by name. handled==true
				// because a rejection must TERMINATE the chain: falling through
				// would hand the call to a later claimant that never accounted
				// for it.
				assert.Truef(t, got.handled, "%s must be CLAIMED and then refused, never fall through", s.name)
				assert.NotContainsf(t, got.body, genericDenyMarker,
					"%s must carry the arm's own refusal, not the generic deny. Observed: %s", s.name, got.body)
				assert.Containsf(t, got.body, s.marker,
					"%s must name the param the arm does not route. Observed: %s", s.name, got.body)
				assert.Zerof(t, got.execDelta, "%s must be refused BEFORE any Execute RPC", s.name)
			case shapeNoReadShape:
				assert.Containsf(t, got.body, genericDenyMarker,
					"%s names no read at all, so the deny is correct. Observed: %s", s.name, got.body)
			}
		})
	}
}

// legacyListProjectsRenders are the two markdown renders the retired
// container-listing intercept produced. Either one appearing in a browse
// response means that carve-out is back and the five container types are once
// again answered outside the engine browse arm.
var legacyListProjectsRenders = []string{
	"Projects & Research",
	"No projects or research documents found",
}

// TestQueryDispatchParity_DecisionBrowseReachesEngine pins where a decision
// browse is answered after the bespoke decisions claimant is retired: the same
// engine browse arm every other node type already uses, inheriting its limit,
// offset, status/meta filtering and filter-correct total.
//
// handled==false is the positive artifact — the retired claimant answered here.
func TestQueryDispatchParity_DecisionBrowseReachesEngine(t *testing.T) {
	c, eng := newParityClient(t)
	got := driveQueryParity(t, c, eng, map[string]any{"type": "decision", "limit": 3})

	assert.Falsef(t, got.handled,
		"a decision browse must DECLINE through the intercept chain and be answered by the engine "+
			"browse arm — a claim here is the bespoke decisions listing. Observed: %s", got.body)
	assert.NotContainsf(t, got.body, genericDenyMarker,
		"a decision browse must be SERVED by the engine, not denied. Observed: %s", got.body)
}

// TestQueryDispatchParity_DecisionTextSearchIsKnowledgeArm is a
// CHARACTERIZATION GUARD: green before the retirement and green after. It does
// NOT test the change.
//
// Its job is to pin the premise the retirement rests on — that a text-bearing
// decision query is already served by the knowledge search arm, so retiring the
// bespoke claimant strands nothing. If the chain order or the knowledge arm's
// claim gate ever moves, the assumption fails loudly here rather than silently
// leaving decision text searches unanswered.
func TestQueryDispatchParity_DecisionTextSearchIsKnowledgeArm(t *testing.T) {
	c, eng := newParityClient(t)
	got := driveQueryParity(t, c, eng, map[string]any{"type": "decision", "text": "caching"})

	assert.Containsf(t, got.body, knowledgeSearchArmMarker,
		"a text-bearing decision query must be served by the knowledge search arm — this is the "+
			"premise that makes retiring the decisions claimant safe. Observed: %s", got.body)
}

// driveSearchParity is the search-shaped sibling of driveQueryParity. The
// `search` tool is a separate compile path from `query`, so its cells are
// driven through the chain under their own tool name rather than by bending the
// query driver. There is no engine fall-through here: the knowledge/default
// search arm claims unconditionally, so an unclaimed search would show up as an
// empty body rather than a deny.
func driveSearchParity(t *testing.T, c *client, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)

	_, _, res := c.runInterceptChain(opCtx(), kgtools.CallToolParams{Name: "search", Arguments: raw})
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

// TestSearchDispatchParity_GraphlessRepoSearchIsCodeArm pins which arm actually
// answers a search whose graph was omitted but whose repo was named. The repo
// gate has already decided that shape targets code; before the fix the search
// arm behind it re-read the raw graph field, saw empty, and served it from the
// knowledge corpus instead.
//
// The second sub-case is a STANDING CONTROL, not decoration.
// knowledgeSearchArmMarker is proven live elsewhere in this file only on
// QUERY-shaped cells, and these two cells reach the arm through a brand-new
// search-shaped driver. Without a case that must SEE the marker, an absent
// marker in the first case could equally mean "the code arm served it" (the
// pass we want) or "this driver never produces the marker at all" (a dead probe
// reading green). Asserting both directions removes that ambiguity on every
// future run rather than once at review time.
func TestSearchDispatchParity_GraphlessRepoSearchIsCodeArm(t *testing.T) {
	t.Run("graphless_repo_is_code_arm", func(t *testing.T) {
		c, _ := newParityClient(t)
		body := driveSearchParity(t, c, map[string]any{"query": "x", "repo": "knowledge"})
		assert.NotContainsf(t, body, knowledgeSearchArmMarker,
			"a graphless search naming a repo must land on the CODE arm — the repo gate already "+
				"decided that. Observed: %s", body)
	})

	t.Run("explicit_knowledge_is_knowledge_arm", func(t *testing.T) {
		c, _ := newParityClient(t)
		body := driveSearchParity(t, c, map[string]any{
			"query": "x", "graph": "knowledge", "repo": "knowledge",
		})
		assert.Containsf(t, body, knowledgeSearchArmMarker,
			"an explicit knowledge search must land on the knowledge arm — without this the "+
				"sibling case could pass on a probe that never produces the marker. Observed: %s", body)
	})
}

// TestSearchDispatchParity_RegisteredGraphTwin drives the registered
// custom-graph read cells through the REAL chain. The per-arm tests in package
// tools prove the claim predicate and the shared tail; only here can it be
// observed that no EARLIER member of runQueryDomainIntercepts takes these shapes
// first, and that the refusal cell is refused pre-Compile rather than after a
// read ran.
//
// The knowledge control is a STANDING CONTROL, not decoration — the same
// construction TestSearchDispatchParity_GraphlessRepoSearchIsCodeArm documents.
// Without a cell that MUST see one marker while the other is absent, an
// assertion about marker absence could pass on a driver that never produces
// either. It also fences the relaxed claim gate: the custom arm must not start
// swallowing knowledge queries.
func TestSearchDispatchParity_RegisteredGraphTwin(t *testing.T) {
	t.Run("custom_default_mode_type_text_is_claimed", func(t *testing.T) {
		c, eng := newParityClient(t)
		got := driveQueryParity(t, c, eng, map[string]any{
			"graph": "hellograph", "name": "demo", "type": "fact", "text": "auth",
		})

		assert.Truef(t, got.handled,
			"a default-mode custom-graph text search carrying a type filter must be CLAIMED — "+
				"declining it drops the shape onto an engine path that hard-errors for a custom "+
				"graph. Observed: %s", got.body)
		assert.Containsf(t, got.body, registeredGraphSearchArmMarker,
			"the claim must land on the registered custom-graph search arm. Observed: %s", got.body)
		assert.Equalf(t, int32(0), got.execDelta,
			"the arm answers before any Execute RPC. Observed: %s", got.body)
	})

	t.Run("custom_explicit_mode_type_text_is_claimed", func(t *testing.T) {
		c, eng := newParityClient(t)
		got := driveQueryParity(t, c, eng, map[string]any{
			"graph": "hellograph", "name": "demo", "type": "fact", "text": "auth", "mode": "text",
		})

		assert.Truef(t, got.handled,
			"an explicit-mode custom-graph text search carrying a type filter must be CLAIMED. "+
				"Observed: %s", got.body)
		assert.Containsf(t, got.body, registeredGraphSearchArmMarker,
			"the claim must land on the registered custom-graph search arm. Observed: %s", got.body)
		assert.Equalf(t, int32(0), got.execDelta,
			"the arm answers before any Execute RPC. Observed: %s", got.body)
	})

	t.Run("custom_id_plus_type_is_refused_by_precheck", func(t *testing.T) {
		c, eng := newParityClient(t)
		got := driveQueryParity(t, c, eng, map[string]any{
			"graph": "hellograph", "name": "demo", "id": "h1", "type": "fact",
		})

		assert.Falsef(t, got.handled,
			"an id-plus-filter payload is a by-id read, not a search — the custom arm must decline "+
				"it. Observed: %s", got.body)
		assert.Containsf(t, got.body, refusedByIDSelectorMarker,
			"the deliberately non-graph-gated by-id refusal covers custom graphs too, which is what "+
				"lets the relaxed claim gate keep declining id shapes without leaving them silently "+
				"unfiltered. Observed: %s", got.body)
		assert.Equalf(t, int32(0), got.execDelta,
			"the refusal happened pre-Compile, not after a read ran. Observed: %s", got.body)
	})

	t.Run("knowledge_control_still_lands_on_the_knowledge_arm", func(t *testing.T) {
		c, eng := newParityClient(t)
		got := driveQueryParity(t, c, eng, map[string]any{
			"graph": "knowledge", "text": "auth", "type": "finding",
		})

		assert.Containsf(t, got.body, knowledgeSearchArmMarker,
			"the equivalent knowledge payload must still land on the knowledge arm. Observed: %s",
			got.body)
		assert.NotContainsf(t, got.body, registeredGraphSearchArmMarker,
			"the relaxed custom-graph gate must not start swallowing knowledge queries. Observed: %s",
			got.body)
	})
}

// TestQueryDispatchParity_TypedStatusBrowseReachesEngine is the chain-level
// half of the status-routing fix. The per-arm test in package tools proves the
// reflective arm releases its claim; only here can it be observed that nothing
// BEHIND that arm picks the shape up instead, and that the engine actually
// serves it rather than meeting it with the generic deny.
//
// handled==false is the positive artifact: before the fix the chain claimed
// this shape and answered it from the thought corpus.
func TestQueryDispatchParity_TypedStatusBrowseReachesEngine(t *testing.T) {
	c, eng := newParityClient(t)
	got := driveQueryParity(t, c, eng, map[string]any{
		"type": "step", "status": "completed", "limit": 2,
	})

	assert.Falsef(t, got.handled,
		"a typed browse carrying a status filter must DECLINE through the intercept chain and be "+
			"answered by the engine browse arm — a claim here routes it to thought recall. Observed: %s",
		got.body)
	assert.NotContainsf(t, got.body, genericDenyMarker,
		"the typed status browse must be SERVED by the engine, not denied. Observed: %s", got.body)
}

// TestQueryDispatchParity_ContainerTypeBrowseFallsToEngine pins the invariant
// the container-listing carve-out violated: a bare type-browse for a CONTAINER
// type must reach the same engine browse arm every other node type already
// uses, so it inherits that arm's limit/offset handling and its response key.
//
// The carve-out claimed these five types and ignored limit and offset entirely,
// which is why every call returned the same fixed page. Asserting arm SELECTION
// rather than row counts is forced by the fixture (countingEngine ignores the
// request and returns a fixed payload) and is sufficient: pagination behavior
// on the engine arm is pinned by TestCompileQuery_BrowseDefaultsLimit, and
// reaching that arm is what makes these types inherit it.
func TestQueryDispatchParity_ContainerTypeBrowseFallsToEngine(t *testing.T) {
	for _, nodeType := range []string{"plan", "project", "ticket", "research", "document"} {
		t.Run(nodeType, func(t *testing.T) {
			c, eng := newParityClient(t)
			got := driveQueryParity(t, c, eng, map[string]any{
				"type": nodeType, "limit": 3, "offset": 20,
			})

			assert.Falsef(t, got.handled,
				"type %q browse must DECLINE through the intercept chain and be answered by the "+
					"engine browse arm — a claim here is the container carve-out. Observed: %s",
				nodeType, got.body)
			assert.NotContainsf(t, got.body, genericDenyMarker,
				"type %q browse must be SERVED by the engine, not denied. Observed: %s", nodeType, got.body)
			for _, legacy := range legacyListProjectsRenders {
				assert.NotContainsf(t, got.body, legacy,
					"type %q browse carries the legacy container-listing render %q. Observed: %s",
					nodeType, legacy, got.body)
			}
		})
	}
}
