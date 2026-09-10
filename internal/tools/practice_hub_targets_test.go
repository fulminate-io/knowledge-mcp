// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_targets_test.go drives the `source_hub` selector on the four
// practice TARGET arms through the REAL mutate intercept: update, which the
// passthrough arm claims; update_batch, bulk_update_metadata and upsert, which
// decline to the engine when they are served. Every row goes through
// InterceptMutate rather than calling a guard directly, so a row measures the arm
// a caller actually reaches.
//
// THE SEED IS ITS OWN, not the edge arms', because these rows need three shapes
// that file has no spelling for: a node carrying source_hub with an EMPTY value
// (distinct from a node carrying no key), a TOMBSTONED member, and an id that
// resolves nowhere, which is the shape every arm's absent-target row drives.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// The seeded practice ids these rows address. "blank-1" carries the hub key with
// an EMPTY value, which is a different state from "orphan-1"'s missing key and is
// the one the present-but-empty arm of the refusal reads.
const (
	hubTargetMemberA  = "pat-1"
	hubTargetMemberB  = "uc-1"
	hubTargetOtherHub = "uc-2"
	hubTargetNoKey    = "orphan-1"
	hubTargetBlankKey = "blank-1"
	hubTargetTomb     = "tomb-1"
	hubTargetAbsent   = "missing-1"
)

// driveHubTarget runs one payload through the real mutate intercept against a
// freshly seeded fake.
func driveHubTarget(t *testing.T, payload string) (*fakeGraphCaller, bool, kgtools.ToolResult) {
	t.Helper()
	fc := practiceHubTargetFake(t)
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate", Arguments: json.RawMessage(payload),
	})
	// AND ON THROUGH THE ENGINE WHEN THE INTERCEPT DECLINES, which is what the
	// daemon does: mcp_client calls the injected Dispatch for every call the
	// chain does not claim. The practice hub rules live in the engine now,
	// beneath every caller, so a driver that stopped at the intercept would
	// report a declining arm as unguarded when the rule that guards it runs one
	// layer down — and would leave that layer unobserved from this package.
	if !handled {
		// ONLY FOR A PAYLOAD THE ENGINE CAN COMPILE. A shape the engine denies
		// outright — an edge write with an empty endpoint, say — has no plan for
		// the hub rules to guard, and adopting its generic deny here would report
		// a row about the intercept's disposition as a row about that deny.
		if _, compiles := engine.Compile("mutate", json.RawMessage(payload)); compiles {
			if dispatched, derr := engine.Dispatch(opCtx(), fc.Execute, fc.Stats, "mutate",
				json.RawMessage(payload)); derr == nil {
				res = dispatched
			}
		}
	}
	// EVERY DRIVE ENDS WITH THE NODE INVARIANT. It is applied here rather than
	// row by row so a row added later inherits it, which is the whole point of
	// asserting the STATE instead of the route. See practice_hub_invariant_test.go.
	assertHubDriveCannotSplit(t, fc, payload, res)
	return fc, handled, res
}

// hubTargetPayload renders the four arms' payloads from ONE builder, so a row
// that names an arm cannot silently drift into testing a different shape. The
// `targets` fragment is spliced verbatim, which is how a row swaps the target.
func hubTargetPayload(operation, hub, targets string) string {
	head := `{"operation":"` + operation + `","graph":"practice"`
	if hub != "" {
		head += `,"source_hub":"` + hub + `"`
	}
	if targets == "" {
		// The by-hub delete names no target at all: the hub IS its selection axis,
		// and a delete carrying both is refused as two selections. An empty
		// fragment must therefore render valid json rather than a trailing comma.
		return head + "}"
	}
	return head + "," + targets + "}"
}

// The per-arm target fragments, one target each unless the row overrides them.
func hubTargetsFor(operation, id string) string {
	switch operation {
	case "update":
		return `"id":"` + id + `","description":"probe-description"`
	case "upsert":
		return `"id":"` + id + `","type":"pattern","name":"probe-name","summary":"probe summary"`
	case "update_batch":
		return `"items":[{"id":"` + id + `","summary":"probe summary"}]`
	case "bulk_update_metadata":
		return `"updates":[{"id":"` + id + `","metadata":{"probe-key":"probe-value"}}]`
	}
	return ""
}

// hubTargetWrittenPlan returns the MutationPlan this drive would apply: the one
// the client issued when the arm CLAIMED the call, and the one the engine
// compiles when it declined. Without the second half a declining arm's row could
// only assert "nothing was written", which is equally true of a refusal.
func hubTargetWrittenPlan(t *testing.T, fc *fakeGraphCaller, payload string) *knowledgev1.MutationPlan {
	t.Helper()
	if len(fc.execMutations) > 0 {
		return fc.execMutations[len(fc.execMutations)-1]
	}
	req, ok := engine.Compile("mutate", json.RawMessage(payload))
	require.True(t, ok, "a declined call must still compile, or nothing was measured")
	m, isMutation := req.GetPlan().(*knowledgev1.ExecuteRequest_Mutation)
	require.True(t, isMutation)
	return m.Mutation
}

func planText(t *testing.T, plan *knowledgev1.MutationPlan) string {
	t.Helper()
	b, err := json.Marshal(plan)
	require.NoError(t, err)
	return string(b)
}

// fetchPracticeNodesForTest issues the SAME bulk read the guard issues, at a
// chosen tombstone visibility. It exists so the tombstone rows can carry their
// own known positive: it drives the production helper, not a copy of it, so a
// harness that did not model tombstones fails this control rather than passing
// every row vacuously.
func fetchPracticeNodesForTest(
	fc *fakeGraphCaller, ids []string, withTombstones bool,
) (map[string]*knowledgev1.Node, bool, error) {
	visibility := foundation.ExcludeTombstones
	if withTombstones {
		visibility = foundation.IncludeTombstones
	}
	return foundation.FetchNodesByIDs(opCtx(), fc, kgtypes.GraphPractice, "", ids, visibility)
}

// hubTargetArms is the population every row below iterates. It is the arm list
// the decided disposition names, and a row that skipped one would be the silent
// drop this whole file exists to close.
var hubTargetArms = []string{"update", "update_batch", "bulk_update_metadata", "upsert"}

// TestPracticeTargetHub_ScopesTheTargets is the decided disposition on the four
// target arms: with a hub named, every target must already be grouped under it.
func TestPracticeTargetHub_ScopesTheTargets(t *testing.T) {
	for _, op := range hubTargetArms {
		t.Run(op+"/target under the hub: the write proceeds", func(t *testing.T) {
			payload := hubTargetPayload(op, hubEndpointsHubA, hubTargetsFor(op, hubTargetMemberA))
			fc, _, res := driveHubTarget(t, payload)
			require.False(t, res.IsError, "the target is grouped under the named hub: %s", toolResultText(res))

			plan := hubTargetWrittenPlan(t, fc, payload)
			assert.Contains(t, planText(t, plan), hubTargetMemberA, "the write still names its target")

			reads := hubEndpointReads(fc)
			require.Len(t, reads, 2,
				"the target scope is ONE bulk read over every target, and the engine adds ONE for the hub")
			assert.Equal(t, []string{hubTargetMemberA}, reads[0],
				"the target scope's read carries the target; the hub's own validity is the engine's")
		})

		t.Run(op+"/a target under another hub is refused naming both hubs", func(t *testing.T) {
			fc, handled, res := driveHubTarget(t,
				hubTargetPayload(op, hubEndpointsHubA, hubTargetsFor(op, hubTargetOtherHub)))
			require.True(t, handled, "a refusal is CLAIMED, never declined to the engine")
			require.True(t, res.IsError, "a target outside the hub is bad input, not a no-op")
			body := toolResultText(res)
			assert.Contains(t, body, hubEndpointsHubA, "the refusal names the hub asked for")
			assert.Contains(t, body, hubTargetOtherHub, "the refusal names the target")
			assert.Contains(t, body, hubEndpointsHubB, "the refusal names the target's ACTUAL hub")
			assert.Empty(t, fc.execMutations, "nothing is written before the refusal")
		})

		t.Run(op+"/a target carrying no hub key is refused naming the absence", func(t *testing.T) {
			fc, handled, res := driveHubTarget(t,
				hubTargetPayload(op, hubEndpointsHubA, hubTargetsFor(op, hubTargetNoKey)))
			require.True(t, handled)
			require.True(t, res.IsError)
			body := toolResultText(res)
			assert.Contains(t, body, hubTargetNoKey)
			assert.Contains(t, body, "no source_hub metadata key",
				"the absence is named as an absence, not reported as a different hub")
			assert.Empty(t, fc.execMutations)
		})

		t.Run(op+"/a target carrying an EMPTY hub value is refused as grouped under none", func(t *testing.T) {
			// The present-but-empty arm. Without its own seeding shape this row is
			// served by the wrong-hub branch and reports a hub of "".
			fc, handled, res := driveHubTarget(t,
				hubTargetPayload(op, hubEndpointsHubA, hubTargetsFor(op, hubTargetBlankKey)))
			require.True(t, handled)
			require.True(t, res.IsError)
			assert.Contains(t, toolResultText(res), "no source_hub metadata key",
				"a key present with an empty value is grouped under NO hub, not under a hub named \"\"")
			assert.Empty(t, fc.execMutations)
		})

		t.Run(op+"/language is refused, naming the replacement", func(t *testing.T) {
			fc, handled, res := driveHubTarget(t,
				`{"operation":"`+op+`","graph":"practice","language":"go",`+
					hubTargetsFor(op, hubTargetMemberA)+`}`)
			require.True(t, handled)
			require.True(t, res.IsError, "`language` on a practice write stays refused")
			assert.Contains(t, toolResultText(res), "source_hub")
			assert.Empty(t, fc.execMutations)
		})
	}

	t.Run("update/a target named twice costs ONE row of the bulk read", func(t *testing.T) {
		// THE DEDUPE IS A GUARD ON THE READ, and nothing observed it: removed, the
		// same id is sent twice and every refusal row above stays green, because
		// the ANSWER does not change — only the read does. A batch that names a
		// thousand targets, half of them repeats, is what the dedupe is for, and
		// the only place its absence is visible is the id list the guard sends.
		fc, _, res := driveHubTarget(t, hubTargetPayload("update", hubEndpointsHubA,
			`"ids":["`+hubTargetMemberA+`","`+hubTargetMemberA+`"],"description":"probe-description"`))
		require.False(t, res.IsError, toolResultText(res))
		reads := hubEndpointReads(fc)
		require.Len(t, reads, 2,
			"the target scope is ONE bulk read however many targets the payload names, plus the engine's hub read")
		assert.Equal(t, []string{hubTargetMemberA}, reads[0],
			"a repeated target is resolved once, not once per mention")
	})

	// The absent-target row splits by arm: all four refuse, but the three pure
	// update arms say "resolves to no node" while upsert names the create that
	// groups instead. Driving them from one loop would hide that split; upsert's
	// row lives in TestPracticeUpsertHub_UnresolvedKeyIsRefusedExistingKeyIsScoped.
	for _, op := range []string{"update", "update_batch", "bulk_update_metadata"} {
		t.Run(op+"/a target absent from the graph is refused", func(t *testing.T) {
			fc, handled, res := driveHubTarget(t,
				hubTargetPayload(op, hubEndpointsHubA, hubTargetsFor(op, hubTargetAbsent)))
			require.True(t, handled)
			require.True(t, res.IsError)
			body := toolResultText(res)
			assert.Contains(t, body, hubTargetAbsent)
			assert.Contains(t, body, "resolves to no node")
			assert.Empty(t, fc.execMutations)
		})
	}

	t.Run("update/every id of an ids[] batch is scoped, and the offender is named by index", func(t *testing.T) {
		fc, handled, res := driveHubTarget(t, hubTargetPayload("update", hubEndpointsHubA,
			`"ids":["`+hubTargetMemberA+`","`+hubTargetOtherHub+`"],"description":"probe-description"`))
		require.True(t, handled)
		require.True(t, res.IsError, "one target outside the hub refuses the whole call")
		assert.Contains(t, toolResultText(res), "`ids[1]`",
			"the refusal names WHICH payload key held the offender")
		assert.Empty(t, fc.execMutations, "no id of a partly-outside batch is written")
	})

	t.Run("update_batch/the offender is named by its items[] index", func(t *testing.T) {
		_, _, res := driveHubTarget(t, hubTargetPayload("update_batch", hubEndpointsHubA,
			`"items":[{"id":"`+hubTargetMemberA+`","summary":"s"},{"id":"`+hubTargetOtherHub+`","summary":"s"}]`))
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "`items[1].id`")
	})

	t.Run("bulk_update_metadata/the offender is named by its updates[] index", func(t *testing.T) {
		_, _, res := driveHubTarget(t, hubTargetPayload("bulk_update_metadata", hubEndpointsHubA,
			`"updates":[{"id":"`+hubTargetMemberA+`","metadata":{"k":"v"}},`+
				`{"id":"`+hubTargetOtherHub+`","metadata":{"k":"v"}}]`))
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "`updates[1].id`")
	})

	t.Run("a hub naming no target at all is refused rather than served whole-graph", func(t *testing.T) {
		fc, handled, res := driveHubTarget(t,
			`{"operation":"update_batch","graph":"practice","source_hub":"`+hubEndpointsHubA+`","items":[]}`)
		require.True(t, handled)
		require.True(t, res.IsError, "a hub with nothing to scope is bad input, not the whole graph")
		assert.Contains(t, toolResultText(res), "names no target")
		assert.Empty(t, fc.execMutations)
	})
}

// TestPracticeTargetHub_OmittedIsTheWholeGraph is the KNOWN POSITIVE for every
// refusal above and the byte-identity row the disposition requires: with no hub
// the MERGE-semantics arms behave exactly as they did before the selector reached
// them — same plan, no hub on the wire, and no read at all.
//
// UPSERT IS DELIBERATELY NOT IN THE LOOP BELOW, and its absence is a decided
// contract rather than a gap. Upsert is the one arm that REPLACES the body's
// metadata instead of merging it, so on a grouped node the hub-less call was not
// a no-op at all: it stripped the `source_hub` key and left the `sourced-from`
// edge behind, un-grouping the node on an ordinary field edit. A practice upsert
// now carries the stored hub forward, which costs it one read on this path — the
// one thing the omitted parameter does NOT leave untouched. Its rows, including
// the byte-identity ones for a new id and for a node under no hub, are in
// practice_hub_upsert_carry_test.go, and the three arms below still state the
// whole-graph reading in full.
func TestPracticeTargetHub_OmittedIsTheWholeGraph(t *testing.T) {
	for _, op := range []string{"update", "update_batch", "bulk_update_metadata"} {
		t.Run(op+"/no source_hub pays no read and carries no hub", func(t *testing.T) {
			// The target sits under hub B while nothing names a hub, so this row
			// states the whole-graph reading as a BEHAVIOUR rather than as an
			// absence of refusals.
			payload := hubTargetPayload(op, "", hubTargetsFor(op, hubTargetOtherHub))
			fc, _, res := driveHubTarget(t, payload)
			require.False(t, res.IsError, "a hub-less write is unchanged: %s", toolResultText(res))
			assert.Empty(t, hubEndpointReads(fc), "and it pays no target-hub read at all")

			body := planText(t, hubTargetWrittenPlan(t, fc, payload))
			assert.NotContains(t, body, kgtypes.MetaKeySourceHub,
				"nothing hub-related reaches the wire when no hub was named")
		})
	}

	for _, op := range []string{"update", "update_batch", "bulk_update_metadata"} {
		t.Run(op+"/the hub scopes client-side and never rides the plan", func(t *testing.T) {
			// The three arms on which the hub is PURELY a scope. The same payload
			// with and without it must compile to the same write, which is what
			// makes "the selector adds a narrowing, never a field" checkable.
			scoped := hubTargetPayload(op, hubEndpointsHubA, hubTargetsFor(op, hubTargetMemberA))
			bare := hubTargetPayload(op, "", hubTargetsFor(op, hubTargetMemberA))
			fcScoped, _, resScoped := driveHubTarget(t, scoped)
			require.False(t, resScoped.IsError, toolResultText(resScoped))
			fcBare, _, resBare := driveHubTarget(t, bare)
			require.False(t, resBare.IsError, toolResultText(resBare))

			assert.Equal(t,
				planText(t, hubTargetWrittenPlan(t, fcBare, bare)),
				planText(t, hubTargetWrittenPlan(t, fcScoped, scoped)),
				"the hub-scoped write is byte-identical to the unscoped one")
		})
	}
}

// TestPracticeUpsertHub_UnresolvedKeyIsRefusedExistingKeyIsScoped is upsert's own
// split, and the unresolved arm of it is a REFUSAL rather than a write.
//
// WHY THE ROW BELOW ASSERTS A REFUSAL. Grouping needs the hub key and the
// node→hub edge in ONE plan, and only the CREATE plan can carry both; an upsert
// whose key does not resolve was therefore re-dispatched as a create. That
// re-dispatch rested on the store failing a duplicate id, and the store does not:
// a create issued on an id that appeared between the guard read and the write
// returns Created, REPLACES the node whole (clearing every field the payload
// omits) and appends a second hub edge, reproduced on an isolated client, and the
// cloud tail is ON CONFLICT DO UPDATE, which overwrites too. So the window is a
// silent whole-node clobber, and the arm refuses instead, naming the create that
// groups.
func TestPracticeUpsertHub_UnresolvedKeyIsRefusedExistingKeyIsScoped(t *testing.T) {
	t.Run("a NEW id is REFUSED naming create as the arm that groups", func(t *testing.T) {
		fc, handled, res := driveHubTarget(t,
			hubTargetPayload("upsert", hubEndpointsHubA, hubTargetsFor("upsert", hubTargetAbsent)))
		require.True(t, handled, "a refusal is CLAIMED, never declined to the engine")
		require.True(t, res.IsError, "an unresolved key under a hub is bad input, not a grouped create")

		body := toolResultText(res)
		assert.Contains(t, body, hubTargetAbsent, "the refusal names the key that did not resolve")
		assert.Contains(t, body, hubEndpointsHubA, "and the hub the caller asked for")
		assert.Contains(t, body, `mutate(create, graph:"practice", source_hub:`,
			"and names the arm that DOES group a new node under a hub")
		assert.Empty(t, fc.execMutations,
			"NOTHING is written: no create is re-dispatched, so no id can be replaced whole on a race")
	})

	t.Run("an EXISTING id under the hub declines, and the plan preserves its membership", func(t *testing.T) {
		payload := hubTargetPayload("upsert", hubEndpointsHubA, hubTargetsFor("upsert", hubTargetMemberA))
		fc, handled, res := driveHubTarget(t, payload)
		assert.False(t, handled, "the update half declines to the engine, which owns the write")
		assert.False(t, res.IsError, toolResultText(res))

		plan := hubTargetWrittenPlan(t, fc, payload)
		require.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, plan.GetKind())
		assert.Equal(t, hubEndpointsHubA, plan.GetNodeBodies()[0].GetMetadata()[kgtypes.MetaKeySourceHub],
			"an upsert WRITES the body's metadata, so the hub key must ride it or the write "+
				"would strip the membership it was scoped by")
	})

	t.Run("an upsert naming no id is refused rather than read", func(t *testing.T) {
		fc, handled, res := driveHubTarget(t,
			`{"operation":"upsert","graph":"practice","source_hub":"`+hubEndpointsHubA+`","type":"pattern"}`)
		require.True(t, handled)
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "upsert key")
		assert.Empty(t, hubEndpointReads(fc), "the missing key is decidable from the payload, so no read is paid")
	})
}

// TestPracticeTargetHub_ReadFailuresRefuse drives the three ways the guard can
// fail to SEE its targets. Each is a refusal rather than a pass, because a guard
// that could not read its targets has not checked them.
func TestPracticeTargetHub_ReadFailuresRefuse(t *testing.T) {
	t.Run("a failed bulk read refuses, naming that nothing was written", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		fc.bulkQueryErr = errors.New("target read failed")
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(hubTargetPayload("update", hubEndpointsHubA,
				hubTargetsFor("update", hubTargetMemberA))),
		})
		require.True(t, handled, "an unreadable scope is never passed on to a path that ignores the hub")
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "could not be read")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("a TRUNCATED read refuses rather than treating an unseen target as absent", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		fc.bulkTruncated = true
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(hubTargetPayload("update_batch", hubEndpointsHubA,
				hubTargetsFor("update_batch", hubTargetMemberA))),
		})
		require.True(t, handled)
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "TRUNCATED")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("the control: the same rows serve when the read succeeds whole", func(t *testing.T) {
		// The same-run KNOWN POSITIVE for the two refusals above: without it a
		// row could be red for any reason and still read as the arm firing.
		for _, op := range []string{"update", "update_batch"} {
			_, _, res := driveHubTarget(t,
				hubTargetPayload(op, hubEndpointsHubA, hubTargetsFor(op, hubTargetMemberA)))
			assert.Falsef(t, res.IsError, "%s must serve on a whole read: %s", op, toolResultText(res))
		}
	})
}

// TestPracticeTargetHub_TombstonedTargetIsStillAMember pins the visibility rule
// the guard reads on, which had no observer at all before the fake modeled
// tombstones: a soft-deleted node still belongs to the hub it was grouped under.
//
// THE CHOICE IT PINS. The guards read with tombstones so they and the sibling
// by-id probes cannot disagree about whether a node exists, and so a by-hub soft
// delete does not newly refuse writes against the nodes it hid.
//
// EACH CALL SITE HAS ITS OWN ROW, and they are named here because a comment once
// claimed one row covered both and it did not. Flipping
// resolvePracticeHubTargets' read (practice_hub_targets.go) to ExcludeTombstones
// reds THIS test; flipping guardPracticeHubScopesEndpoints' read
// (practice_hub_endpoints.go) reds
// TestPracticeEndpointHub_TombstonedEndpointIsStillAMember, and nothing else in
// the tools or engine suites, which is why that second row had to be written.
func TestPracticeTargetHub_TombstonedTargetIsStillAMember(t *testing.T) {
	for _, op := range hubTargetArms {
		t.Run(op+"/a tombstoned member is served, not refused as absent", func(t *testing.T) {
			_, _, res := driveHubTarget(t,
				hubTargetPayload(op, hubEndpointsHubA, hubTargetsFor(op, hubTargetTomb)))
			assert.False(t, res.IsError,
				"the guard reads WITH tombstones, so a soft-deleted member is still under its hub: %s",
				toolResultText(res))
		})
	}

	t.Run("the control: the harness really does hide a tombstone from a tombstone-blind read", func(t *testing.T) {
		// The same-run known positive for the rows above. Without it, "the guard
		// served the tombstoned target" is equally true of a harness that never
		// modeled tombstones at all — which is exactly the state that left this
		// choice unobserved.
		fc := practiceHubTargetFake(t)
		blind, _, err := fetchPracticeNodesForTest(fc, []string{hubTargetTomb}, false)
		require.NoError(t, err)
		assert.NotContains(t, blind, hubTargetTomb, "a tombstone-blind read must MISS the tombstoned id")
		seeing, _, err := fetchPracticeNodesForTest(fc, []string{hubTargetTomb}, true)
		require.NoError(t, err)
		assert.Contains(t, seeing, hubTargetTomb, "and a tombstone-including read must serve it")
	})
}
