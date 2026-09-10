// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_upsert_carry_test.go drives the ONE practice arm that PRESERVES a
// membership instead of scoping it: mutate(upsert) carrying NO `source_hub`.
//
// WHY THE ROWS EXIST. An upsert WRITES the body's metadata rather than merging
// into what is stored, and upsert is the only arm with those semantics — update,
// update_batch and bulk_update_metadata all merge per key. So a hub-LESS upsert
// of a grouped node used to replace its metadata with the payload's, dropping the
// `source_hub` key while the `sourced-from` edge survived: the node kept its edge,
// lost its key, and a hub-scoped browse — which predicates on the key — stopped
// returning it. Nothing refused, nothing read, and no row watched.
//
// EVERY ROW GOES THROUGH THE REAL INTERCEPT, on the seeded target fake, so a row
// measures the arm a caller reaches rather than a helper a caller cannot address.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// hubCarryPayload renders a hub-LESS practice upsert of one id, optionally
// carrying its own body metadata. It is spelled here rather than reusing
// hubTargetPayload because these rows are ABOUT the omitted parameter, and a
// builder that can add one is a builder a row can accidentally use.
func hubCarryPayload(id, metadata string) string {
	body := `{"operation":"upsert","graph":"practice","id":"` + id +
		`","type":"pattern","name":"probe-name","summary":"probe summary"`
	if metadata != "" {
		body += "," + metadata
	}
	return body + "}"
}

// hubCarriedHub reads the `source_hub` the drive's plan would write. It reads the
// CLIENT-ISSUED plan when the arm claimed the call and the compiled one when it
// declined, for hubTargetWrittenPlan's reason: a declining arm's row could
// otherwise only assert "nothing was written", which is equally true of a refusal.
func hubCarriedHub(t *testing.T, fc *fakeGraphCaller, payload string) string {
	t.Helper()
	plan := hubTargetWrittenPlan(t, fc, payload)
	bodies := plan.GetNodeBodies()
	require.Len(t, bodies, 1, "an upsert plan carries exactly one body")
	return bodies[0].GetMetadata()[kgtypes.MetaKeySourceHub]
}

// TestPracticeUpsertHub_HubLessUpsertKeepsTheMembership is the decided contract:
// a practice upsert never un-groups a node. An existing member's stored hub is
// carried onto the body whether or not the call names one, so no field edit can
// split the metadata key from the sourced-from edge.
func TestPracticeUpsertHub_HubLessUpsertKeepsTheMembership(t *testing.T) {
	t.Run("a grouped member keeps its hub with no param and no metadata", func(t *testing.T) {
		payload := hubCarryPayload(hubTargetMemberA, "")
		fc, handled, res := driveHubTarget(t, payload)
		require.True(t, handled,
			"the carry-forward is applied client-side, so the call is claimed rather than passed on unstamped")
		require.False(t, res.IsError, "an ordinary field edit of a member is not an error: %s", toolResultText(res))

		assert.Equal(t, hubEndpointsHubA, hubCarriedHub(t, fc, payload),
			"the stored membership must ride the body the upsert writes, or the write strips it")

		reads := hubEndpointReads(fc)
		require.Len(t, reads, 2,
			"the carry reads the member's stored hub, and the engine then holds that hub to the hub contract "+
				"on the stamped payload — two rules over two id sets")
		assert.Equal(t, []string{hubTargetMemberA}, reads[0], "the carry reads the upsert key, nothing else")
		assert.Equal(t, []string{hubEndpointsHubA}, reads[1], "and the engine reads the hub it stamped")
	})

	t.Run("the hub carried forward is the TARGET's own, never another node's", func(t *testing.T) {
		// The wrong-hub mutation's observer. A carry that stamped a constant, the
		// first seeded hub, or another row's id would satisfy the row above, which
		// happens to name the same hub the fake's first member carries.
		payload := hubCarryPayload(hubTargetOtherHub, "")
		fc, _, res := driveHubTarget(t, payload)
		require.False(t, res.IsError, toolResultText(res))
		assert.Equal(t, hubEndpointsHubB, hubCarriedHub(t, fc, payload),
			"a member of hub B keeps hub B; carrying any other node's hub would MOVE it")
	})

	t.Run("a tombstoned member is still a member and keeps its hub", func(t *testing.T) {
		// The visibility rule, stated on this path too: the carry reads WITH
		// tombstones, as every sibling hub read does, so a soft-deleted node that
		// is upserted back does not lose the collection it was grouped under.
		payload := hubCarryPayload(hubTargetTomb, "")
		fc, _, res := driveHubTarget(t, payload)
		require.False(t, res.IsError, toolResultText(res))
		assert.Equal(t, hubEndpointsHubA, hubCarriedHub(t, fc, payload))
	})
}

// TestPracticeUpsertHub_HubLessUpsertChangesNothingElse is the byte-identity half
// and the known-positive frame for the rows above: on every hub-less shape with
// NO membership to preserve, the arm declines exactly as it did before, so the
// engine compiles the caller's own payload and the plan is the base one.
func TestPracticeUpsertHub_HubLessUpsertChangesNothingElse(t *testing.T) {
	for _, row := range []struct{ name, id string }{
		{"a NEW id has no membership to preserve", hubTargetAbsent},
		{"a node carrying no hub key is grouped under none", hubTargetNoKey},
		{"a node carrying an EMPTY hub value is grouped under none", hubTargetBlankKey},
	} {
		t.Run(row.name, func(t *testing.T) {
			payload := hubCarryPayload(row.id, "")
			fc, handled, res := driveHubTarget(t, payload)
			assert.False(t, handled, "with nothing to preserve the arm declines to the engine as before")
			assert.False(t, res.IsError, toolResultText(res))
			// THE WRITE IS THE ENGINE'S, not one the intercept added: a declined
			// call is dispatched by the daemon, and this driver models that. What
			// the row asserts is the PLAN — the caller's own payload, unstamped.
			assert.Empty(t, hubCarriedHub(t, fc, payload),
				"nothing hub-related is invented for a node that is under no hub")
		})
	}

	t.Run("a body naming the hub the node is ALREADY under is a second spelling", func(t *testing.T) {
		// The body repeats a fact rather than changing one, so the write proceeds
		// and the carry has nothing to add. This is the accepting half of the rule
		// whose refusing half is the row below.
		payload := hubCarryPayload(hubTargetMemberA, `"metadata":{"`+kgtypes.MetaKeySourceHub+`":"`+hubEndpointsHubA+`"}`)
		fc, handled, res := driveHubTarget(t, payload)
		assert.False(t, handled, "a body that already names the stored hub is not this arm's business")
		assert.False(t, res.IsError, toolResultText(res))
		assert.Equal(t, hubEndpointsHubA, hubCarriedHub(t, fc, payload),
			"the caller's own key rides the write, naming the hub the node is under")
	})

	t.Run("a body naming ANOTHER hub is refused: a member is not MOVED on this surface", func(t *testing.T) {
		// THE ROW THIS ONE REPLACES asserted the opposite, and the shipped help
		// and schema said it was how a node moves between hubs. It is not: the
		// upsert writes the metadata key and carries no `sourced-from` edge, so
		// the node ended up filed under hub B by its metadata and linked to hub A
		// by its edge — the split state requirement 2 forbids, reached from a
		// third direction. There is no arm that rewrites both carriers together,
		// so there is no move to offer, and the honest answer is the refusal.
		fc, handled, res := driveHubTarget(t,
			hubCarryPayload(hubTargetMemberA, `"metadata":{"`+kgtypes.MetaKeySourceHub+`":"`+hubEndpointsHubB+`"}`))
		require.True(t, handled, "a body that would repoint the key is claimed, not passed on")
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, hubEndpointsHubB, "the refusal names the hub the body asked for")
		assert.Contains(t, body, hubEndpointsHubA, "and the hub the node is actually under")
		assert.Contains(t, body, "not MOVED between hubs", "and says the operation is not offered")
		assert.Empty(t, fc.execMutations, "nothing is written")
	})

	t.Run("a hub-less CREATE carrying a body hub is sent to the parameter", func(t *testing.T) {
		// The other route to a split, and the one that was reachable at base: a
		// create whose body names a hub lands the metadata key with NO edge,
		// because only the call's parameter emits one.
		fc, handled, res := driveHubTarget(t,
			`{"operation":"create","graph":"practice","type":"pattern","name":"n","summary":"s",`+
				`"metadata":{"`+kgtypes.MetaKeySourceHub+`":"`+hubEndpointsHubA+`"}}`)
		require.True(t, handled)
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, practiceHubParamOnWrites, "the refusal names the parameter that groups")
		assert.Contains(t, body, string(kgtypes.EdgeSourcedFrom), "and the edge the body cannot draw")
		assert.Empty(t, fc.execMutations)

		// The control: the same create with the hub as the PARAMETER serves, and
		// its plan carries both carriers.
		_, _, ok := driveHubTarget(t,
			`{"operation":"create","graph":"practice","source_hub":"`+hubEndpointsHubA+
				`","type":"pattern","name":"n","summary":"s"}`)
		assert.False(t, ok.IsError, toolResultText(ok))
	})

	t.Run("an upsert missing its type pays no read", func(t *testing.T) {
		// A shape that cannot compile can never write, so it earns no round trip:
		// the deny it gets downstream is the same one it always got.
		fc, _, _ := driveHubTarget(t,
			`{"operation":"upsert","graph":"practice","id":"`+hubTargetMemberA+`","name":"n"}`)
		assert.Empty(t, hubEndpointReads(fc), "a payload the engine cannot compile buys no membership read")
	})

	t.Run("an upsert naming no id at all pays no read", func(t *testing.T) {
		// The other half of the same precondition, and its own row because an
		// EMPTY id is not the same shape as a missing type: it resolves nowhere by
		// construction, so a read for it can only ever come back empty. Buying that
		// round trip is spending a request to be told what the payload already
		// said.
		fc, _, _ := driveHubTarget(t,
			`{"operation":"upsert","graph":"practice","type":"pattern","name":"n","summary":"s"}`)
		assert.Empty(t, hubEndpointReads(fc), "an empty upsert key resolves nowhere, so it buys no read")
	})

	t.Run("an operation that is not an upsert is not carried, however its payload is shaped", func(t *testing.T) {
		// THE OPERATION CONJUNCT'S OWN ROW. This arm's param spec consumes the
		// WHOLE schema, so nothing rejects an unlink that also carries `id` and
		// `type` — and the carry's other conjuncts are all satisfied by such a
		// payload. Without the operation test that unlink would be read, stamped
		// and CLAIMED, so a caller's edge write would be re-dispatched carrying a
		// metadata key it never asked for. Only the arm whose semantics REPLACE the
		// body's metadata needs preserving; every other arm merges.
		fc, handled, res := driveHubTarget(t,
			`{"operation":"unlink","graph":"practice","id":"`+hubTargetMemberA+`","type":"pattern",`+
				`"from":"`+hubTargetMemberA+`","to":"`+hubTargetMemberB+`","relationship":"contains"}`)
		assert.False(t, handled, "a hub-less practice unlink declines, whatever else its payload carries")
		assert.False(t, res.IsError, toolResultText(res))
		assert.Empty(t, hubEndpointReads(fc), "and it buys no membership read: an unlink writes no body")
	})

	t.Run("an upsert on ANOTHER family reads no practice hub and carries none", func(t *testing.T) {
		// THE FAMILY CONJUNCT'S OWN ROW, and it is not decoration: this arm is
		// reached by checks, code, cloud and every other non-knowledge family, and
		// node ids are not family-scoped. Without the conjunct a checks upsert of
		// an id that happens to name a practice MEMBER would read the practice
		// graph and stamp a practice `source_hub` onto a checks node — the same
		// family-blind stamping the off-family refusal was written to end, arrived
		// at through the carry instead of through the parameter. The id below is
		// deliberately a seeded practice member, so the row states a behaviour
		// rather than the absence of a seed.
		payload := `{"operation":"upsert","graph":"` + checksGraphSelector + `","id":"` + hubTargetMemberA +
			`","type":"pattern","name":"probe-name","summary":"probe summary"}`
		fc, handled, res := driveHubTarget(t, payload)
		assert.False(t, handled, "a checks upsert is not this arm's business")
		assert.False(t, res.IsError, toolResultText(res))
		assert.Empty(t, hubEndpointReads(fc),
			"no family but practice has source hubs, so none of them buys a practice membership read")
		assert.NotContains(t, planText(t, hubTargetWrittenPlan(t, fc, payload)), kgtypes.MetaKeySourceHub,
			"and no practice hub key reaches a foreign family's node")
	})

	t.Run("a hub-CARRYING upsert is untouched by this path", func(t *testing.T) {
		// The round-3 shape, restated here as the boundary: the parameter path
		// already stamps through the engine's own lowering, and it must keep
		// paying exactly its own one read rather than two.
		payload := hubTargetPayload("upsert", hubEndpointsHubA, hubTargetsFor("upsert", hubTargetMemberA))
		fc, handled, res := driveHubTarget(t, payload)
		assert.False(t, handled, "the hub-scoped upsert still declines to the engine, which owns the write")
		assert.False(t, res.IsError, toolResultText(res))
		assert.Equal(t, hubEndpointsHubA, hubCarriedHub(t, fc, payload))
		assert.Len(t, hubEndpointReads(fc), 2,
			"the scope guard's read and the engine's hub read — the carry itself adds none")
	})

	t.Run("the whole-graph reading holds on every OTHER practice family arm", func(t *testing.T) {
		// The claim this change narrows: "omitted means the whole practice graph"
		// is unchanged everywhere except the upsert body's membership key. Driven
		// on a member of hub B so a row states a behaviour rather than an absence.
		for _, op := range []string{"update", "update_batch", "bulk_update_metadata"} {
			payload := hubTargetPayload(op, "", hubTargetsFor(op, hubTargetOtherHub))
			fc, _, res := driveHubTarget(t, payload)
			assert.Falsef(t, res.IsError, "%s: a hub-less write is unchanged: %s", op, toolResultText(res))
			assert.Emptyf(t, hubEndpointReads(fc), "%s: and pays no hub read at all", op)
			assert.NotContainsf(t, planText(t, hubTargetWrittenPlan(t, fc, payload)), kgtypes.MetaKeySourceHub,
				"%s: nothing hub-related reaches the wire when no hub was named", op)
		}
	})
}

// TestPracticeUpsertHub_UnreadableMembershipRefuses drives the two ways the carry
// can fail to SEE the stored membership. Each REFUSES rather than proceeding,
// because proceeding is the silent strip this path exists to close: a write that
// went out with the key missing is indistinguishable from the defect.
func TestPracticeUpsertHub_UnreadableMembershipRefuses(t *testing.T) {
	for _, row := range []struct {
		name, want string
		seed       func(*fakeGraphCaller)
	}{
		{"a failed read", "could not be read", func(fc *fakeGraphCaller) {
			fc.bulkQueryErr = errors.New("membership read failed")
		}},
		{"a TRUNCATED read", "TRUNCATED", func(fc *fakeGraphCaller) { fc.bulkTruncated = true }},
	} {
		t.Run(row.name+" refuses with nothing written", func(t *testing.T) {
			fc := practiceHubTargetFake(t)
			row.seed(fc)
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate", Arguments: json.RawMessage(hubCarryPayload(hubTargetMemberA, "")),
			})
			require.True(t, handled, "an unreadable membership is never passed on to a write that would strip it")
			require.True(t, res.IsError)
			body := toolResultText(res)
			assert.Contains(t, body, row.want, "the refusal names WHY the membership could not be read")
			assert.Contains(t, body, kgtypes.MetaKeySourceHub,
				"and names the key at stake, so the caller knows what the refusal protects")
			assert.Empty(t, fc.execMutations, "nothing is written")
		})
	}

	t.Run("the control: the same call serves when the read succeeds whole", func(t *testing.T) {
		// The same-run known positive: without it either row above could be red
		// for any reason and still read as the refusal firing.
		_, _, res := driveHubTarget(t, hubCarryPayload(hubTargetMemberA, ""))
		assert.False(t, res.IsError, "a whole read serves: %s", toolResultText(res))
	})
}
