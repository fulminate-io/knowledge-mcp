// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_message_surface_test.go holds the rows about what a caller is
// TOLD, as distinct from what is written: the refusal an empty edge endpoint
// earns, and the two shipped documentation surfaces. Split out of
// practice_hub_targets_test.go to keep both inside the repo's per-file length
// ceiling.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestPracticeLinkHub_EmptyEndpointNamesTheEmptyEndpoint pins the refusal an
// empty `from` or `to` gets on a hub-carrying practice link. Before it, the
// cross-graph composer declined on the empty endpoint and the generic link
// fall-through's rejection reason told the caller to make the practice link it
// was already making.
func TestPracticeLinkHub_EmptyEndpointNamesTheEmptyEndpoint(t *testing.T) {
	for _, row := range []struct{ name, payload, wantRole string }{
		{"empty from", `"from":"","to":"` + hubTargetMemberB + `"`, "`from`"},
		{"empty to", `"from":"` + hubTargetMemberA + `","to":""`, "`to`"},
	} {
		t.Run(row.name, func(t *testing.T) {
			fc, handled, res := driveHubTarget(t,
				`{"operation":"link","graph":"practice",`+row.payload+
					`,"relationship":"contains","source_hub":"`+hubEndpointsHubA+`"}`)
			require.True(t, handled)
			require.True(t, res.IsError)
			body := toolResultText(res)
			assert.Contains(t, body, row.wantRole, "the refusal names WHICH endpoint is empty")
			assert.Contains(t, body, "EMPTY")
			assert.NotContains(t, body, "is not applied by this path",
				"the caller must not be told the param is unapplied on the call it just made")
			assert.Empty(t, fc.execMutations)
		})
	}

	t.Run("the control: both endpoints named under the hub still links", func(t *testing.T) {
		_, _, res := driveHubTarget(t,
			`{"operation":"link","graph":"practice","from":"`+hubTargetMemberA+`","to":"`+hubTargetMemberB+
				`","relationship":"contains","source_hub":"`+hubEndpointsHubA+`"}`)
		assert.False(t, res.IsError, toolResultText(res))
	})
}

// TestPracticeLinkHub_EmptyEndpointGateIsSilentWithNoHub is the omitted-parameter
// control for refusePracticeHubEmptyEndpoint, and it was the one hub gate whose
// empty-hub self-filter had no observer: with that filter removed, the whole tools
// and engine suites stayed green while a practice link or unlink carrying an empty
// endpoint and NO hub flipped from DECLINED to claimed-and-refused by a message
// naming a `source_hub` of "". A caller who typed no hub would be told about an
// empty one, and the call would be claimed away from the validation that speaks
// for it.
//
// THE ROW STATES THE DISPOSITION, not the message: handled=false and no error is
// what "the gate returns before deciding anything" looks like from outside. Its
// same-run KNOWN POSITIVE is the identical payload WITH a hub, which must be
// claimed and refused — without that half a gate deleted outright would satisfy
// every row here.
func TestPracticeLinkHub_EmptyEndpointGateIsSilentWithNoHub(t *testing.T) {
	for _, op := range []string{"link", "unlink"} {
		for _, row := range []struct{ name, endpoints string }{
			{"empty from", `"from":"","to":"` + hubTargetMemberB + `"`},
			{"empty to", `"from":"` + hubTargetMemberA + `","to":""`},
		} {
			t.Run(op+"/"+row.name+"/no hub is not this gate's business", func(t *testing.T) {
				// THE DISPOSITION A CALLER SEES comes first, driven through the
				// real intercept: declined, so the engine's own from/to validation
				// is what speaks about the empty endpoint. It is asserted BEFORE
				// the gate-level row below because that row is fatal, and a change
				// that breaks both should report both rather than only the second.
				fc, handled, res := driveHubTarget(t,
					`{"operation":"`+op+`","graph":"practice",`+row.endpoints+`,"relationship":"contains"}`)
				assert.Falsef(t, handled,
					"a hub-less %s with an empty endpoint is declined, not claimed by the hub gate", op)
				assert.Falsef(t, res.IsError,
					"and it is not refused by a message naming an empty source_hub: %s", toolResultText(res))
				assert.Empty(t, fc.execMutations, "the declined call issues no client-side write")

				// AND THE GATE, by name: an empty hub must return before deciding
				// anything, whichever endpoint is empty.
				require.NoError(t, refusePracticeHubEmptyEndpoint(mutateArgs{Operation: op, Graph: "practice"}),
					"a call naming no hub must not be claimed by a hub gate")
			})

			t.Run(op+"/"+row.name+"/known positive: the same call WITH a hub is refused", func(t *testing.T) {
				_, handled, res := driveHubTarget(t,
					`{"operation":"`+op+`","graph":"practice",`+row.endpoints+
						`,"relationship":"contains","source_hub":"`+hubEndpointsHubA+`"}`)
				require.True(t, handled,
					"known positive: with a hub named the gate must claim, or the silence above proves nothing")
				require.True(t, res.IsError)
				assert.Contains(t, toolResultText(res), "EMPTY", "and it must name the empty endpoint")
			})
		}
	}
}

// TestPracticeTargetHub_DocumentedOnBothShippedSurfaces holds the two surfaces a
// caller actually reads to the behaviour this file pins. Both are asserted
// because a fix to one is invisible to the other: help("mutate") renders the
// constant, and the tool definition ships the wire schema's description.
//
// It asserts on the RENDERED help rather than on the source line, so a block
// moved or reformatted still has to say the thing.
func TestPracticeTargetHub_DocumentedOnBothShippedSurfaces(t *testing.T) {
	handled, res := InterceptHelp(opCtx(), interceptTestDeps{}, kgtools.CallToolParams{
		Name: "help", Arguments: json.RawMessage(`{"topic":"mutate"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, toolResultText(res))
	rendered := toolResultText(res)
	require.NotEmpty(t, rendered, "help(mutate) rendered nothing — the assertions below would be vacuous")

	for _, want := range []string{
		"update_batch", "bulk_update_metadata", "SCOPE OVER THE TARGETS", "updates[].id",
	} {
		assert.Containsf(t, rendered, want,
			"the rendered mutate help must document the hub's meaning on the target arms (missing %q)", want)
	}
	// UPSERT GROUPS NOTHING, and both surfaces have to say so. The arm once
	// re-dispatched an unresolved key as a create; that re-dispatch is gone,
	// because a create on an id that appeared in the meantime replaces the node
	// whole. A reader still told "a new id is the create half and is GROUPED"
	// would be told the opposite of what the tool now does.
	assert.NotContains(t, rendered, "GROUPED under the hub",
		"the rendered help must not still promise that an upsert groups a new id")
	assert.Contains(t, rendered, "an upsert NEVER groups",
		"and must state that grouping is the create arm's job, not the upsert's")

	// THE BODY COLLISION, on both surfaces for the same reason: a caller composing
	// a create_batch from rows that already carry a source_hub key has to be told
	// that the key and the parameter must agree BEFORE the run refuses, not after.
	for _, want := range []string{"names a DIFFERENT hub", "nodes[]"} {
		assert.Containsf(t, rendered, want,
			"the rendered mutate help must document the body collision (missing %q)", want)
	}

	schema := mutateProperties()["source_hub"].Description
	require.NotEmpty(t, schema, "the wire schema declares no source_hub description")
	for _, want := range []string{"UPDATE_BATCH", "BULK_UPDATE_METADATA", "UPSERT", "items[].id"} {
		assert.Containsf(t, schema, want,
			"the mutate wire schema must document the hub on the target arms (missing %q)", want)
	}
	assert.Contains(t, schema, "An UPSERT never GROUPS",
		"the wire schema must say the same thing the rendered help does about upsert")
	assert.NotContains(t, schema, "is the create half and is GROUPED under the hub",
		"and must not still carry the retired create-half promise")
	for _, want := range []string{"names a DIFFERENT hub", "nodes[]", "updates[]"} {
		assert.Containsf(t, schema, want,
			"the mutate wire schema must document the body collision (missing %q)", want)
	}

	// MEMBERSHIP IS PRESERVED, on BOTH surfaces, swept as one joined block so a
	// sentence added to the help and forgotten in the schema fails. The rule is
	// the one thing an omitted `source_hub` does NOT leave untouched, and a reader
	// still told "omitted means the whole practice graph, byte for byte" would
	// believe a hub-less field edit costs no read and writes what it was handed.
	joined := map[string]string{`help("mutate")`: rendered, "the wire schema's source_hub description": schema}
	for surface, body := range joined {
		for _, want := range []string{
			"NEVER UN-GROUPS", "whether or not the call names one", "one read",
			// The round-7 rules, on the same joined sweep.
			// The round-9 rules replace the round-7 one: the delete is ONE write
			// now, so "not atomic" is gone and the self-key that makes it one is
			// what both surfaces must state.
			"NEVER WRITTEN BY HAND", "GROUPED UNDER NOTHING BUT ITSELF", "ONE write",
			// AND THE ROUND-6 RULES, on the same joined sweep. A hub must name a
			// live hub; a body alone never groups; and a member is never moved
			// between hubs — the last replaces a sentence both surfaces carried
			// that said the opposite, so its ABSENCE is asserted below too.
			"NAME A HUB", "A BODY ALONE NEVER GROUPS", "NEVER MOVED BETWEEN HUBS",
		} {
			assert.Containsf(t, body, want,
				"%s must state that a practice upsert preserves membership (missing %q)", surface, want)
		}
	}

	// THE RETIRED SENTENCE, on both surfaces. It told the reader that writing a
	// source_hub key into a body is how a node moves between hubs; the run that
	// closed this class showed that write splits the node instead, leaving the key
	// on one hub and the edge on another. A doc the change made false is the
	// requirement-15 class, so its absence is asserted rather than assumed.
	for surface, body := range joined {
		assert.NotContainsf(t, body, "moves a node between hubs",
			"%s must not still promise that a body key MOVES a member", surface)
		// AND THE RETIRED SECOND WRITE. An earlier shape removed the hub by id
		// after its members and told the reader the pair was not atomic; the hub
		// is swept by its own key in one write now, and a doc still warning about
		// the split would describe a behaviour the code no longer has.
		assert.NotContainsf(t, body, "not atomic",
			"%s must not still warn about a second write that is gone", surface)
	}

	// SAME-RUN KNOWN POSITIVE for the sweep above: with the sentence holed out of
	// each surface the same assertion must fail, so a green run is evidence the
	// literal is present rather than evidence the matcher never looks.
	for surface, body := range joined {
		holed := strings.ReplaceAll(body, "NEVER UN-GROUPS", "")
		assert.NotContainsf(t, holed, "NEVER UN-GROUPS",
			"known positive: with the rule removed from %s the assertion must no longer hold", surface)
	}
}
