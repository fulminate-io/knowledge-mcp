// SPDX-License-Identifier: Apache-2.0

// collect_recipe_land_versioning_test.go — requirements 4, 5, 7, 8 and 11 of the
// landing: the versioned twin, the response's disclosure, the hand-edited
// resident, the three refusal arms and the refused render parameters, plus the
// two input classes the matrix names that no requirement cell owns on its own
// (the collision read's page boundary, and a soft-deleted resident).
//
// The fake and its helpers are in collect_recipe_land_harness_test.go.

package tools

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// ---------------------------------------------------------------------------
// Requirement 4 — the versioned twin
// ---------------------------------------------------------------------------

// residentPattern is a machine-landed row already in the combined graph under the
// id this recipe's "Message Router" emission mints.
//
// IT CARRIES NO VERSION KEY, and that is the only state production ever puts on a
// BASE id: a first landing writes no version at all, and every version after it
// lives on a TWIN under a derived id. A helper that took a version parameter
// would invite a fixture seeded with a state production never writes, which is
// exactly the vacuous-test class the three-run cell replaced.
func residentPattern() *knowledgev1.Node {
	id := recipe.StableID(landingTargetKey(), "hohpe-eip", "pattern", "Message Router")
	return &knowledgev1.Node{
		Id: id, Type: "pattern", SymbolName: "Message Router",
		Metadata: map[string]string{kgtypes.MetaKeySourceHub: wantHubID()},
	}
}

func TestInterceptCollect_Landing_ResidentIdLandsAVersionedTwin(t *testing.T) {
	residentID := recipe.StableID(landingTargetKey(), "hohpe-eip", "pattern", "Message Router")

	t.Run("a first collision lands v2 with one next-version edge old to new", func(t *testing.T) {
		c := newLandingCaller()
		c.practiceByID[residentID] = residentPattern()
		body, plan := runLand(t, c, nil)

		wantTwin := recipe.VersionedTwinID(landingTargetKey(), "hohpe-eip", "pattern", residentID, 2)
		var twin *knowledgev1.NodeBody
		for _, b := range plan.GetNodeBodies() {
			assert.NotEqual(t, residentID, b.GetId(),
				"THE RESIDENT IS NEVER IN THE BATCH: create_batch is an add, so writing its id would overwrite it")
			if b.GetId() == wantTwin {
				twin = b
			}
		}
		require.NotNil(t, twin, "the collision must land a twin under a DISTINCT id")
		assert.Equal(t, "Message Router", twin.GetName(), "carrying the same content")
		assert.Equal(t, "2", twin.GetMetadata()[landingVersionMetaKey],
			"an absent version on the resident reads as version 1, so its twin is 2")

		var versionEdges int
		for _, e := range plan.GetEdges() {
			if e.GetType() != string(kgtypes.EdgeNextVersion) {
				continue
			}
			versionEdges++
			assert.Equal(t, residentID, e.GetFromId(), "the edge runs FROM the old version")
			assert.Equal(t, int32(-1), e.GetFromIdx(), "which is an existing node, addressed by id")
			assert.GreaterOrEqual(t, e.GetToIdx(), int32(0), "TO the new one, which is a slot in this batch")
		}
		assert.Equal(t, 1, versionEdges, "exactly one next-version edge per twin")

		assert.Contains(t, body, "twins=1", "and the response names the twin")
	})

	t.Run("the collision read is a BULK read per chain level against the TARGET graph", func(t *testing.T) {
		c := newLandingCaller()
		c.practiceByID[residentID] = residentPattern()
		runLand(t, c, nil)

		// TWO reads, and the second is not a per-id loop: it is one bulk probe of
		// the NEXT chain level for every id that collided at the level above. The
		// walk costs one read per level, never one per row — a regression to a
		// per-id loop is invisible in the landing's outcome, which is why the
		// count is asserted at all.
		var byIDReads []landingRead
		for _, r := range c.reads {
			if r.graph == string(kgtypes.GraphPractice) && len(r.ids) > 0 {
				byIDReads = append(byIDReads, r)
				assert.Empty(t, r.name, "the combined practice graph carries no instance name")
				assert.Empty(t, r.language)
			}
		}
		require.Len(t, byIDReads, 2,
			"one bulk hydrate of the emitted ids, then one bulk probe of the v2 ids that ends the walk")
		assert.Contains(t, byIDReads[0].ids, residentID,
			"the WHOLE emitted id set is looked up, so a resident on any row is found")
		assert.Contains(t, byIDReads[1].ids,
			recipe.VersionedTwinID(landingTargetKey(), "hohpe-eip", "pattern", residentID, 2),
			"and the probe asks for the DERIVED v2 id, which a base-id read can never see")
	})
}

// TestInterceptCollect_Landing_TruncatedCollisionReadRefuses is the cell nothing
// else catches.
//
// foundation.FetchNodesByIDs REPORTS truncation rather than erroring, by design,
// because a renderer legitimately shows a short list as short. This caller is the
// other kind: a partial resident map means residents it did not see, and the
// add-not-upsert write then overwrites exactly those. So the verdict is FATAL
// here, and discarding it produces a successful-looking run that destroys data.
func TestInterceptCollect_Landing_TruncatedCollisionReadRefuses(t *testing.T) {
	c := newLandingCaller()
	c.truncateByIDs = true
	msg := refuseLand(t, c, nil)

	assert.Contains(t, strings.ToLower(msg), "truncat", "the refusal names the truncation it refused on")
	assert.Contains(t, strings.ToLower(msg), "resident",
		"and states why a partial read is fatal here: an unseen resident would be overwritten")
}

// ---------------------------------------------------------------------------
// Requirement 5 — the response discloses hub, counts, twins and refusals
// ---------------------------------------------------------------------------

func TestInterceptCollect_Landing_ResponseDisclosesHubAndCounts(t *testing.T) {
	t.Run("a landing reports the hub, the landed count and the twin count", func(t *testing.T) {
		c := newLandingCaller()
		body, _ := runLand(t, c, nil)

		assert.Contains(t, body, "landed:", "the response leads with a landing header, not an extract one")
		assert.Contains(t, body, wantHubID(), "and names the hub every node was grouped under")
		assert.Contains(t, body, "nodes=2", "the landed count is the WHOLE emitted set")
		assert.Contains(t, body, "twins=0")
		assert.Contains(t, body, "hub=created")
	})

	t.Run("a body matching nothing is distinguishable from one that landed nothing", func(t *testing.T) {
		// THE SOURCE GRAPH STILL HOLDS SECTIONS. A body whose select names a type
		// the source does not carry at all is REFUSED before the walk, which is a
		// different outcome and a pre-existing one; the state under test here is a
		// body that selects real rows and FILTERS all of them away.
		c := newLandingCaller()
		deps := &recipeDeps{sink: &recipeCaptureSink{}, gc: c}
		handled, res := InterceptCollect(opCtx(), deps, landParams(t, map[string]any{
			"recipe_body": "select section\n" +
				`filter {"matches": {"of": "section.symbol_name", "regex": "^NoSuchHeadingAnywhere"}}` + "\n" +
				"emit pattern {\n    type := \"pattern\"\n    name := section.symbol_name\n}",
		}))
		require.True(t, handled)
		require.False(t, res.IsError, "a body that matches nothing is not an error: %s", resultText(res))

		body := resultText(res)
		assert.Contains(t, body, "nodes=0")
		assert.Contains(t, body, "matched=0",
			"a run that MATCHED nothing must be distinguishable from one that matched rows and landed none")
	})
}

// ---------------------------------------------------------------------------
// Requirement 7 — a hand-edited resident is never modified
// ---------------------------------------------------------------------------

// TestInterceptCollect_Landing_HandEditedResidentIsNeverInTheBatch is the
// regression the deleted write guard existed to prevent, and the cell that must
// never be dropped.
//
// IT IS ASSERTED BY SELECTOR AS WELL AS BY OUTCOME, because the guard IS the
// read: a collision read issued against knowledge/default answers "absent" for
// every emitted id, so an outcome-only assertion would pass while the hand-edited
// node was being overwritten.
func TestInterceptCollect_Landing_HandEditedResidentIsNeverInTheBatch(t *testing.T) {
	residentID := recipe.StableID(landingTargetKey(), "hohpe-eip", "pattern", "Message Router")
	hand := residentPattern()
	hand.Description = "a human wrote this"
	hand.Content = "and this"

	c := newLandingCaller()
	c.practiceByID[residentID] = hand
	_, plan := runLand(t, c, nil)

	for _, b := range plan.GetNodeBodies() {
		assert.NotEqual(t, residentID, b.GetId(),
			"the hand-edited row's id must not appear in an add-not-upsert batch")
	}
	assert.Equal(t, "a human wrote this", hand.GetDescription(),
		"and the resident object itself is untouched by the landing")

	var sawTargetRead bool
	for _, r := range c.reads {
		if r.graph == string(kgtypes.GraphPractice) && len(r.ids) > 0 {
			sawTargetRead = true
		}
		assert.NotEmpty(t, r.graph,
			"no read may fall through to the knowledge default: every landing read names its graph")
	}
	assert.True(t, sawTargetRead,
		"THE SELECTOR LEG: the collision read must have been issued against the practice graph at all")
}

// ---------------------------------------------------------------------------
// Requirement 8 — three refusal arms, each before any write
// ---------------------------------------------------------------------------

func TestInterceptCollect_Landing_RefusesBeforeAnyWrite(t *testing.T) {
	t.Run("the combined practice graph does not exist yet", func(t *testing.T) {
		c := newLandingCaller()
		c.practiceAbsent = true
		msg := refuseLand(t, c, nil)
		assert.Contains(t, msg, "practice", "the refusal names the graph that is absent")
		assert.Contains(t, strings.ToLower(msg), "does not exist")
	})

	t.Run("the raw graph's origin read FAILED", func(t *testing.T) {
		// THE HUB PROBE MUST SUCCEED FIRST, or this cell asserts the wrong
		// refusal. A nil GraphCaller — the other way rawGraphRecordedSource
		// reports a failure rather than "nothing recorded" — fails the
		// target-graph probe that runs BEFORE it, so a cell built on one passes
		// whatever the origin read does. The raw read is failed on its own
		// instead, with a resident hub so the probe ahead of it is clean.
		c := newLandingCaller()
		seedResidentHub(c, wantHubID())
		c.rawReadErr = true

		msg := refuseLand(t, c, nil)
		assert.Contains(t, strings.ToLower(msg), "recorded source",
			"the refusal names what could not be read, rather than reporting an empty origin as a fact")
		assert.Contains(t, msg, "hohpe-eip", "and names the raw graph it failed on")

		// THE CONTROL, same fake and same run: with the raw read healthy the same
		// call SUCCEEDS, so the refusal above is the read failure rather than the
		// landing being broken.
		ok := newLandingCaller()
		seedResidentHub(ok, wantHubID())
		runLand(t, ok, nil)
	})

	t.Run("the raw graph root carries NO recorded source key", func(t *testing.T) {
		c := newLandingCaller()
		c.sourceNodes = []*knowledgev1.Node{
			{Id: "root", Type: "page", SymbolName: "EIP"}, // no seed_host
			{Id: "s1", Type: "section", SymbolName: "Message Router"},
		}
		msg := refuseLand(t, c, nil)
		assert.Contains(t, strings.ToLower(msg), "re-collect",
			"the refusal names the repair for a legacy graph collected before its family recorded a source")
		assert.Contains(t, msg, "seed_host",
			"and names the key that is missing, so the caller can check it")
	})
}

// ---------------------------------------------------------------------------
// Requirement 11 — the render parameters are refused on a landing run
// ---------------------------------------------------------------------------

func TestInterceptCollect_Landing_RefusesRowWindowParams(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  []string
	}{
		{"max_rows", map[string]any{"max_rows": 5}, []string{"max_rows"}},
		{"offset", map[string]any{"offset": 3}, []string{"offset"}},
		{"both", map[string]any{"max_rows": 5, "offset": 3}, []string{"max_rows", "offset"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newLandingCaller()
			msg := refuseLand(t, c, tc.extra)
			for _, want := range tc.want {
				assert.Contains(t, msg, want, "the refusal names the offending param")
			}
			assert.Contains(t, msg, "extract",
				"and names the call that works: the preview of what would land IS the extract run")
			assert.Zero(t, c.execCalls,
				"THE PLACEMENT LEG: the refusal fires before any read, so no source read is paid for")
		})
	}

	t.Run("the positive half: a landing with neither writes the WHOLE emitted set", func(t *testing.T) {
		c := newLandingCaller()
		body, plan := runLand(t, c, nil)
		assert.Len(t, plan.GetNodeBodies(), 3, "two emitted rows plus the hub — the whole population, not a page")
		assert.Contains(t, body, "nodes=2")
	})

	t.Run("the arm boundary: an EXTRACT run still honors both params", func(t *testing.T) {
		c := newLandingCaller()
		deps := &recipeDeps{sink: &recipeCaptureSink{}, gc: c}
		args, err := json.Marshal(map[string]any{
			"type": "web", "id": "hohpe-eip", "transformer": "recipe",
			"recipe_body": landingBody, "extract": true, "max_rows": 1, "offset": 0,
		})
		require.NoError(t, err)
		handled, res := InterceptCollect(opCtx(), deps, kgtools.CallToolParams{Name: "collect", Arguments: args})
		require.True(t, handled)
		require.False(t, res.IsError,
			"max_rows and offset stay fully live on the extract arm: the refusal is conditioned on `land`, never on the params")
		assert.Contains(t, resultText(res), "rows=1/2", "and the row cap really did apply")
	})
}

// ---------------------------------------------------------------------------
// Input classes the matrix names
// ---------------------------------------------------------------------------

// TestInterceptCollect_Landing_PagesTheCollisionReadBeyondFiveHundred drives the
// input class at and beyond the bulk read's page boundary.
//
// paging.BrowsePageSize is 500, so an emitted set larger than that pages — and a
// resident on the SECOND page has to be found, or its row is written as a base
// node over a resident the read never asked about.
func TestInterceptCollect_Landing_PagesTheCollisionReadBeyondFiveHundred(t *testing.T) {
	const rows = 600
	c := newLandingCaller()
	c.sourceNodes = []*knowledgev1.Node{
		{Id: "root", Type: "page", SymbolName: "EIP", Metadata: map[string]string{"seed_host": "example.org"}},
	}
	for i := range rows {
		c.sourceNodes = append(c.sourceNodes, &knowledgev1.Node{
			Id: "s" + strconv.Itoa(i), Type: "section", SymbolName: "Section " + strconv.Itoa(i),
		})
	}
	// A resident BEYOND the first page's worth of ids.
	lateID := recipe.StableID(landingTargetKey(), "hohpe-eip", "pattern", "Section 599")
	c.practiceByID[lateID] = &knowledgev1.Node{
		Id: lateID, Type: "pattern", SymbolName: "Section 599",
		Metadata: map[string]string{kgtypes.MetaKeySourceHub: wantHubID(), landingVersionMetaKey: "1"},
	}

	body, plan := runLand(t, c, nil)

	var byIDReads []landingRead
	for _, r := range c.reads {
		if r.graph == string(kgtypes.GraphPractice) && len(r.ids) > 0 {
			byIDReads = append(byIDReads, r)
			assert.LessOrEqual(t, len(r.ids), 500, "every page stays under the server's row ceiling")
		}
	}
	require.Len(t, byIDReads, 3,
		"600 emitted ids drain in ceil(600/500) = 2 pages, then ONE probe of the next chain level")
	assert.Equal(t, 600, len(byIDReads[0].ids)+len(byIDReads[1].ids), "the two pages cover the whole emitted set")
	assert.Len(t, byIDReads[2].ids, 1,
		"the chain probe carries only the ids that COLLIDED — one row here, not the population again")

	wantTwin := recipe.VersionedTwinID(landingTargetKey(), "hohpe-eip", "pattern", lateID, 2)
	assert.NotNil(t, bodyByID(plan, wantTwin),
		"a resident on the SECOND page is still found, so its row lands as a twin rather than over it")
	assert.Contains(t, body, "twins=1")
}

// TestInterceptCollect_Landing_SoftDeletedResidentIsSeen states the tombstone
// policy and asserts it, rather than inheriting a positional literal.
//
// THE CHOICE IS IncludeTombstones, and it is forced rather than preferred. A
// soft-deleted resident read under ExcludeTombstones reads as ABSENT, so the
// landing would mint a base node under an id a tombstoned row still holds — and
// requirement 6's soft-by-default by-hub delete makes exactly that state
// reachable in ordinary use.
func TestInterceptCollect_Landing_SoftDeletedResidentIsSeen(t *testing.T) {
	residentID := recipe.StableID(landingTargetKey(), "hohpe-eip", "pattern", "Message Router")
	dead := residentPattern()
	dead.TombstonedAt = 1

	c := newLandingCaller()
	c.practiceByID[residentID] = dead
	_, plan := runLand(t, c, nil)

	var sawTombstoneRequest bool
	for _, b := range plan.GetNodeBodies() {
		assert.NotEqual(t, residentID, b.GetId(),
			"a tombstoned resident still holds its id, so a base node under it would collide with the shadow")
	}
	wantTwin := recipe.VersionedTwinID(landingTargetKey(), "hohpe-eip", "pattern", residentID, 2)
	assert.NotNil(t, bodyByID(plan, wantTwin), "a soft-deleted resident is a collision, so its row lands as a twin")

	for _, r := range c.reads {
		if r.graph == string(kgtypes.GraphPractice) && len(r.ids) > 0 {
			sawTombstoneRequest = true
		}
	}
	assert.True(t, sawTombstoneRequest, "the collision read really was issued")
}

// bodyByID finds a created body by its id.
func bodyByID(plan *knowledgev1.MutationPlan, id string) *knowledgev1.NodeBody {
	for _, b := range plan.GetNodeBodies() {
		if b.GetId() == id {
			return b
		}
	}
	return nil
}
