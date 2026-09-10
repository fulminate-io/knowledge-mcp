// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_body_conflict_test.go drives the last disposition of `source_hub`
// this branch had left open: the hub key inside a node BODY disagreeing with the
// hub the CALL names.
//
// WHY IT IS A SEPARATE CLASS FROM EVERY ROW BESIDE IT. The sibling files ask what
// each ARM does with the parameter — group, scope, select, refuse. This one asks
// what happens when the payload names the hub TWICE and the two spellings
// disagree, which is a property of the BODY rather than of the arm: the same
// conflict is expressible on a create's `metadata`, on each `nodes[i].metadata`
// of a create_batch, on an upsert's body, and on each item of the two batch
// update arms. A gate written per-arm would close whichever body a fixture
// happened to carry and leave the others open, which is exactly the state this
// file was written to end: a create_batch body naming another hub was ACCEPTED,
// landed carrying the foreign hub id in metadata while its sourced-from edge
// pointed at the hub the call named, and a hub-scoped browse then filed it under
// a hub it is not edged to.
//
// THE CONTROL ROWS ARE NOT DECORATION. A gate that refused every body carrying
// the key at all would pass every refusal row here and silently break the two
// shapes that must keep working: a body naming the SAME hub (a second spelling
// of one fact, not a conflict) and a body naming a hub on a call that names
// none (an ordinary metadata write, which this gate has no business touching).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// hubBodyCompanionKey rides every body fragment below beside the hub key, and it
// is what makes the hub-less CONTROL expressible on all six arms: a
// bulk_update_metadata whose only key was the hub compiles to nothing once the
// key is removed, so the control would measure an empty payload rather than the
// same call without a hub.
const hubBodyCompanionKey = `"probe-key":"probe-value"`

// hubBodyMetaFragment renders the metadata fragment a caller types to name a hub
// inside a body. It is built from the production key constant rather than from
// the literal, so a renamed key moves these rows with it.
func hubBodyMetaFragment(hub string) string {
	return `"metadata":{` + hubBodyCompanionKey + `,"` + kgtypes.MetaKeySourceHub + `":"` + hub + `"}`
}

// hubBodyMetaFragmentHubless is the same fragment with the hub key gone and every
// other key untouched.
func hubBodyMetaFragmentHubless() string {
	return `"metadata":{` + hubBodyCompanionKey + `}`
}

// hubBodyConflictRow is one payload that names a hub twice, plus the payload path
// its refusal has to name. The path is the whole point of the row: `metadata`,
// `nodes[1].metadata` and `items[0].metadata` are three different edits, and a
// message that said only "a body" would name none of them.
type hubBodyConflictRow struct {
	name string
	// conflicting names a body hub that DIFFERS from the call's; agreeing names
	// the same one. The two are rendered from one template so the pair differs in
	// the hub value alone.
	payload func(bodyHub string) string
	role    string
	// stamps marks the arms whose engine lowering STAMPS the call's hub onto the
	// body it writes (the two create shapes and upsert). On those, a body naming
	// the same hub must land byte-identically to a body naming none. The three
	// update arms stamp nothing, so there the key is an ordinary metadata write
	// and the two plans must DIFFER.
	stamps bool
}

// hubBodyConflictRows is the census: every practice write arm whose payload can
// carry a metadata map, with the conflict placed in a body that arm actually
// reads.
//
// THE THREE THE OWED ITEM NAMES come first — create, create_batch and the update
// half of upsert. The two batch update arms follow because their `items[]` and
// `updates[]` entries are the same carrier in a different key, and a rule that
// held on three of five carriers would be a rule a reader cannot enumerate.
func hubBodyConflictRows() []hubBodyConflictRow {
	return []hubBodyConflictRow{
		{
			name:   "create",
			stamps: true,
			role:   "metadata." + kgtypes.MetaKeySourceHub,
			payload: func(bodyHub string) string {
				return hubTargetPayload("create", hubEndpointsHubA,
					`"type":"pattern","name":"probe-name","summary":"probe summary",`+
						hubBodyMetaFragment(bodyHub))
			},
		},
		{
			// THE BODY IS NOT THE ONLY BODY, on purpose: the conflicting one sits
			// BETWEEN two valid ones, so a gate that only ever read nodes[0] — the
			// shape a single-body fixture cannot tell apart — fails this row.
			name:   "create_batch",
			stamps: true,
			role:   "nodes[1].metadata." + kgtypes.MetaKeySourceHub,
			payload: func(bodyHub string) string {
				return hubTargetPayload("create_batch", hubEndpointsHubA,
					`"nodes":[{"type":"pattern","name":"n0","summary":"s0"},`+
						`{"type":"pattern","name":"n1","summary":"s1","id":"body-1",`+
						hubBodyMetaFragment(bodyHub)+`},`+
						`{"type":"pattern","name":"n2","summary":"s2"}]`)
			},
		},
		{
			// The UPDATE half: the key resolves and is grouped under the call's
			// hub, so the target scope passes and the only thing left to refuse is
			// the body. A row driven on an unresolved key would earn the
			// create-naming refusal instead and prove nothing about this class.
			name:   "upsert",
			stamps: true,
			role:   "metadata." + kgtypes.MetaKeySourceHub,
			payload: func(bodyHub string) string {
				return hubTargetPayload("upsert", hubEndpointsHubA,
					`"id":"`+hubTargetMemberA+`","type":"pattern","name":"probe-name",`+
						`"summary":"probe summary",`+hubBodyMetaFragment(bodyHub))
			},
		},
		{
			name: "update",
			role: "metadata." + kgtypes.MetaKeySourceHub,
			payload: func(bodyHub string) string {
				return hubTargetPayload("update", hubEndpointsHubA,
					`"id":"`+hubTargetMemberA+`","description":"probe-description",`+
						hubBodyMetaFragment(bodyHub))
			},
		},
		{
			name: "update_batch",
			role: "items[0].metadata." + kgtypes.MetaKeySourceHub,
			payload: func(bodyHub string) string {
				return hubTargetPayload("update_batch", hubEndpointsHubA,
					`"items":[{"id":"`+hubTargetMemberA+`","summary":"probe summary",`+
						hubBodyMetaFragment(bodyHub)+`}]`)
			},
		},
		{
			name: "bulk_update_metadata",
			role: "updates[0].metadata." + kgtypes.MetaKeySourceHub,
			payload: func(bodyHub string) string {
				return hubTargetPayload("bulk_update_metadata", hubEndpointsHubA,
					`"updates":[{"id":"`+hubTargetMemberA+`",`+hubBodyMetaFragment(bodyHub)+`}]`)
			},
		},
	}
}

// TestPracticeHubBody_ConflictingBodyHubIsRefused is cells (a), (b) and (c) of
// the owed item plus the two sibling carriers: a body hub that differs from the
// call's is refused naming the body, its hub and the call's, with nothing
// written.
func TestPracticeHubBody_ConflictingBodyHubIsRefused(t *testing.T) {
	for _, row := range hubBodyConflictRows() {
		t.Run(row.name, func(t *testing.T) {
			payload := row.payload(hubEndpointsHubB)
			fc, handled, res := driveHubTarget(t, payload)

			require.True(t, handled, "a refusal is CLAIMED, never declined to the engine")
			require.Truef(t, res.IsError,
				"a body naming another hub is bad input, not a value to prefer silently: %s",
				toolResultText(res))
			body := toolResultText(res)
			assert.Contains(t, body, row.role,
				"the refusal must name the payload path the caller has to edit")
			assert.Contains(t, body, hubEndpointsHubA, "the refusal names the hub the CALL asked for")
			assert.Contains(t, body, hubEndpointsHubB, "the refusal names the hub the BODY asked for")
			assert.Empty(t, fc.execMutations, "nothing was written")
		})
	}
}

// TestPracticeHubBody_ConflictNamesTheBodyID is the id half of "naming the body's
// index or id": a create_batch body that carries its own id has that id in the
// refusal, because on a batch composed by a driver the index is a position in a
// generated list and the id is the row the author can find.
func TestPracticeHubBody_ConflictNamesTheBodyID(t *testing.T) {
	_, _, res := driveHubTarget(t, hubBodyConflictRows()[1].payload(hubEndpointsHubB))
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "body-1",
		"a body carrying an id is named by it, not only by its slot")
}

// TestPracticeHubBody_AgreeingBodyHubIsNotAConflict is cell (d): a body naming
// the SAME hub is a second spelling of one fact and is not refused.
//
// THE PLAN COMPARISON IS THE ASSERTION, not a bare "no error". A gate that
// refused the agreeing body would fail on the error alone, but a gate that
// STRIPPED the agreeing key would pass that weaker check while changing what
// lands; only a plan-to-plan comparison catches it. On the arms whose lowering
// stamps the hub the comparison is EQUALITY — the agreeing key is exactly what
// the stamp would have written — and on the three update arms it is INEQUALITY,
// because there the key is the caller's own metadata write and dropping it would
// be a silent edit of the payload.
func TestPracticeHubBody_AgreeingBodyHubIsNotAConflict(t *testing.T) {
	for _, row := range hubBodyConflictRows() {
		t.Run(row.name, func(t *testing.T) {
			agreeing := row.payload(hubEndpointsHubA)
			fc, _, res := driveHubTarget(t, agreeing)
			require.Falsef(t, res.IsError,
				"a body naming the hub the call names is not a conflict: %s", toolResultText(res))
			withKey := planText(t, hubTargetWrittenPlan(t, fc, agreeing))
			assert.Contains(t, withKey, hubEndpointsHubA, "the hub named twice reaches the plan")

			hubless := strings.Replace(agreeing,
				hubBodyMetaFragment(hubEndpointsHubA), hubBodyMetaFragmentHubless(), 1)
			require.NotEqual(t, agreeing, hubless, "the hub-less payload must actually differ from the payload")
			fcHubless, _, resHubless := driveHubTarget(t, hubless)
			require.Falsef(t, resHubless.IsError, "the control call must proceed: %s", toolResultText(resHubless))
			hublessPlan := planText(t, hubTargetWrittenPlan(t, fcHubless, hubless))

			if !row.stamps {
				assert.NotEqual(t, hublessPlan, withKey,
					"this arm stamps nothing, so the caller's own key must still be on the plan")
				return
			}
			assert.Equal(t, hublessPlan, withKey,
				"an agreeing body hub changes nothing about what lands")
		})
	}
}

// TestPracticeHubBody_AbsentBodyHubIsStamped is cell (e): a create whose body
// names no hub is stamped by the call's, exactly as before this gate existed.
func TestPracticeHubBody_AbsentBodyHubIsStamped(t *testing.T) {
	payload := hubTargetPayload("create_batch", hubEndpointsHubA,
		`"nodes":[{"type":"pattern","name":"n0","summary":"s0"}]`)
	fc, _, res := driveHubTarget(t, payload)
	require.Falsef(t, res.IsError, "a body carrying no hub key is the ordinary create: %s", toolResultText(res))

	plan := planText(t, hubTargetWrittenPlan(t, fc, payload))
	assert.Contains(t, plan, kgtypes.MetaKeySourceHub, "the call's hub is stamped onto the body")
	assert.Contains(t, plan, hubEndpointsHubA, "and it is the hub the call named")
}

// TestPracticeHubBody_NoCallHubIsRefusedOnTheCreateShapes is cell (f), and it
// states the OPPOSITE of what it once did.
//
// WHAT IT USED TO ASSERT, AND WHY THAT WAS WRONG. A bare `metadata.source_hub` on
// a call naming no hub was treated as an ordinary metadata write and reached the
// plan untouched. It is not ordinary: grouping is the metadata key AND the
// node→hub `sourced-from` edge, and only the call's parameter emits the edge. So
// that write landed a node carrying a hub id in its metadata with nothing linking
// it there — a hub-scoped browse counted it as a member and a traverse from it
// reached no hub. The caller is now sent to the parameter, which writes both.
//
// IT CARRIES ITS OWN KNOWN POSITIVE in the same run and through the same
// instrument: the identical body WITH an agreeing call hub must SERVE. Without
// that half a gate that refused every create_batch would satisfy the row.
func TestPracticeHubBody_NoCallHubIsRefusedOnTheCreateShapes(t *testing.T) {
	bodyOnly := hubTargetPayload("create_batch", "",
		`"nodes":[{"type":"pattern","name":"n0","summary":"s0",`+
			hubBodyMetaFragment(hubEndpointsHubB)+`}]`)
	require.NotContains(t, bodyOnly, hubEndpointsHubA,
		"the payload must genuinely name no call hub — only the body's own, which is a different value")

	fc, handled, res := driveHubTarget(t, bodyOnly)
	require.True(t, handled, "a body-only hub on a create is claimed, not passed on")
	require.Truef(t, res.IsError,
		"a body key alone writes the metadata without the edge, which is not a grouping: %s",
		toolResultText(res))
	body := toolResultText(res)
	assert.Contains(t, body, practiceHubParamOnWrites, "the refusal names the parameter that groups")
	assert.Contains(t, body, string(kgtypes.EdgeSourcedFrom), "and the edge the body alone cannot draw")
	assert.Empty(t, fc.execMutations, "nothing is written")

	agreeing := hubTargetPayload("create_batch", hubEndpointsHubB,
		`"nodes":[{"type":"pattern","name":"n0","summary":"s0",`+
			hubBodyMetaFragment(hubEndpointsHubB)+`}]`)
	fcPositive, _, positive := driveHubTarget(t, agreeing)
	require.Falsef(t, positive.IsError,
		"known positive: the same body WITH the matching call hub must serve, or the refusal above "+
			"proves nothing: %s", toolResultText(positive))
	assert.Contains(t, planText(t, hubTargetWrittenPlan(t, fcPositive, agreeing)), hubEndpointsHubB,
		"and that write carries the hub")
}

// TestPracticeHubBody_EveryBodyCarrierIsRead is the NO-SKIP guard for the census
// above. The rows could pass while a carrier the payload can hold went unread,
// so the arms are counted against the operations that CAN carry a body at all:
// the two create shapes, upsert, and the three update arms.
func TestPracticeHubBody_EveryBodyCarrierIsRead(t *testing.T) {
	declared := map[string]bool{}
	for _, row := range hubBodyConflictRows() {
		declared[row.name] = true
	}
	for _, op := range []string{
		"create", "create_batch", "upsert", "update", "update_batch", "bulk_update_metadata",
	} {
		assert.Truef(t, declared[op],
			"mutate operation %q carries a metadata body and has no conflict row — an unread carrier is "+
				"the silent drop this file exists to prevent", op)
	}
	// AND THE ARMS THAT CARRY NO BODY stay out: a conflict row on link, unlink or
	// delete would assert against a payload shape no caller can send.
	for _, op := range []string{"link", "unlink", "delete", "answer"} {
		assert.Falsef(t, declared[op],
			"operation %q carries no metadata body, so a conflict row for it would be fiction", op)
	}
}

// TestPracticeHubBody_ConflictIsRefusedBeforeAnyRead is the ordering the file's
// own rule requires: the conflict is decidable from the payload, so it is named
// without spending a round trip. A caller whose payload is wrong on its face
// hears so for free.
func TestPracticeHubBody_ConflictIsRefusedBeforeAnyRead(t *testing.T) {
	for _, row := range hubBodyConflictRows() {
		t.Run(row.name, func(t *testing.T) {
			fc, _, res := driveHubTarget(t, row.payload(hubEndpointsHubB))
			require.True(t, res.IsError)
			assert.Empty(t, hubEndpointReads(fc),
				"a payload-decidable refusal must not pay the hub resolution read")
		})
	}
}

// TestPracticeHubBody_MalformedBodiesAreTheDispatchersError holds the seam the
// sibling target guard already holds: a payload whose body array does not parse
// is reported by the dispatcher, not preempted by this gate with a second
// message about the same malformed json.
func TestPracticeHubBody_MalformedBodiesAreTheDispatchersError(t *testing.T) {
	_, handled, res := driveHubTarget(t,
		`{"operation":"create_batch","graph":"practice","source_hub":"`+hubEndpointsHubA+
			`","nodes":"not-an-array"}`)
	if handled && res.IsError {
		assert.NotContains(t, toolResultText(res), "name different hubs",
			"a malformed nodes[] is a decode error, not a hub conflict")
	}
}

// TestPracticeHubBody_TheGateIsPayloadDecidable pins the gate's own contract at
// the function rather than through the intercept: an empty hub and a foreign
// family both return before anything is compared, and the same args with a
// practice graph and a conflicting body refuse. Without the positive half the
// two nils would be satisfied by a gate that decided nothing at all.
func TestPracticeHubBody_TheGateIsPayloadDecidable(t *testing.T) {
	conflicting := map[string]string{kgtypes.MetaKeySourceHub: hubEndpointsHubB}

	require.NoError(t, guardPracticeHubBodyHubs(mutateArgs{
		Operation: "create", Graph: string(kgtypes.GraphPractice), Metadata: conflicting,
	}), "a call naming no hub has no two spellings to disagree")

	require.NoError(t, guardPracticeHubBodyHubs(mutateArgs{
		Operation: "create", Graph: string(kgtypes.GraphKnowledge),
		SourceHub: hubEndpointsHubA, Metadata: conflicting,
	}), "an off-family call is the off-family gate's to refuse, not this one's")

	require.Error(t, guardPracticeHubBodyHubs(mutateArgs{
		Operation: "create", Graph: string(kgtypes.GraphPractice),
		SourceHub: hubEndpointsHubA, Metadata: conflicting,
		raw: json.RawMessage(`{"operation":"create","graph":"practice"}`),
	}), "known positive: the practice call with two disagreeing spellings must refuse")
}
