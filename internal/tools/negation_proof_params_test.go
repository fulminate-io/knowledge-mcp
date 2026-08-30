// SPDX-License-Identifier: Apache-2.0

package tools

// negation_proof_params_test.go pins the CONTRACT of the two negation-gate
// proof-of-work params, verified_quote and cited_range: that both tools DECLARE
// them, that every mutate dispatch arm CLASSIFIES them, that no wire field on
// either arg struct is schema-invisible, that the gate's rejection says WHERE to
// put the quote, and that a non-negation call carrying one is refused loudly
// rather than accepted and ignored.
//
// The shared TestNegationProofParams_ prefix is load-bearing: the plan's gates
// select on it and assert a count of five, so a renamed or added test fails
// loudly instead of silently shrinking the suite.
//
// Not parallel by construction: the fakes accumulate call state on
// unsynchronised slices, so t.Parallel() would race them (the sibling parity
// harness states the same at mutate_arm_parity_test.go:10-11).

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// proofParams are the two gate-only params this file is about. Both are read by
// InterceptNegationGate before any write and neither is ever persisted.
var proofParams = []string{"verified_quote", "cited_range"}

// negationProofConsumedArms is the exact set of dispatch arms a negation call
// can SELECT, and therefore the exact set that consumes the proof params as a
// reachability discriminant. Derived from recognizeNegationOp (negation_gate.go):
// a mutate negation is a link with relationship=="contradicts" or an update with
// status=="invalidated", so it is the two link arms, the three single-id update
// arms such a call can reach, and the two catch-alls.
//
// Pinned as a literal SITE LIST rather than a count: swapping one arm for
// another must fail, which a count cannot catch.
var negationProofConsumedArms = []armID{
	armLinkCrossGraph, armLinkFallthrough,
	armUpdateBackend, armUpdateTyped, armUpdateFallthrough,
	armGraphPassthrough, armNonKnowledgeFallthrough,
}

// TestNegationProofParams_DeclaredInEverySchema is the schema-invisibility
// reproduction. The gate reads both params off a TOP-LEVEL key on the call, but
// neither tool's InputSchema declared them — so every reader of tools/list
// concluded the param did not exist and reached for metadata or edge_evidence
// instead.
//
// Asserted against the rendered kgtools.Property rather than by greping source:
// the Descriptions are Go string literals a future author may re-wrap, and a
// source grep would be a scheduled false failure on correct work.
func TestNegationProofParams_DeclaredInEverySchema(t *testing.T) {
	schemas := []struct {
		tool  string
		props map[string]kgtools.Property
	}{
		{"mutate", mutateProperties()},
		{"thoughts", ThoughtsToolDef().InputSchema.Properties},
	}
	for _, schema := range schemas {
		for _, param := range proofParams {
			t.Run(schema.tool+"/"+param, func(t *testing.T) {
				prop, ok := schema.props[param]
				require.Truef(t, ok,
					"the %s schema must DECLARE %q — the gate reads it as a top-level param, "+
						"and an undeclared param is invisible to every reader of tools/list",
					schema.tool, param)
				assert.Equal(t, "string", prop.Type, "%s/%s is a string param", schema.tool, param)
				assert.Containsf(t, prop.Description, "TOP-LEVEL",
					"%s/%s must state WHERE the param goes — naming the location is the whole point",
					schema.tool, param)
				assert.Containsf(t, prop.Description, "verbatim substring",
					"%s/%s must state that the proof is a verbatim substring of current source",
					schema.tool, param)
			})
		}
	}
}

// TestNegationProofParams_ClassifiedOnEveryArm proves the two params are
// accounted on all 22 dispatch arms, and that the CONSUMED set is exactly the
// arms a negation call can select. An unclassified (arm, param) pair is the
// silent-drop shape the accounting gate exists to close: a key with no cell can
// never be rejected, so it is accepted and dropped.
func TestNegationProofParams_ClassifiedOnEveryArm(t *testing.T) {
	for _, param := range proofParams {
		t.Run(param, func(t *testing.T) {
			var consumed []armID
			for arm := range mutateArmRegistry {
				class, classified := paramClassFor(arm, param)
				assert.Truef(t, classified,
					"arm %q leaves %q UNCLASSIFIED — a param in no set is neither routed nor "+
						"rejected by declaration, only by accident", arm, param)
				if classified && class == classConsumed {
					consumed = append(consumed, arm)
				}
			}
			assert.ElementsMatch(t, negationProofConsumedArms, consumed,
				"%q must be consumed by exactly the arms a negation call can select", param)
		})
	}
}

// TestNegationProofParams_NoUndeclaredWireField is the DURABLE class gate: it
// fails for any FUTURE wire field added to either arg struct without a matching
// schema declaration, not just for the two this changeset closes.
//
// The single exemption is earned rather than asserted: `alternatives` rides the
// shared mutateArgs wire mirror but belongs to record_decision, and the test
// proves that tool declares it. The exemption map is size-pinned so a future
// undeclared field cannot be waved through by quietly widening the list.
func TestNegationProofParams_NoUndeclaredWireField(t *testing.T) {
	exemptions := map[string]string{
		"alternatives": "declared on record_decision (RecordDecisionToolDef), which shares the mutateArgs wire mirror",
	}
	require.Len(t, exemptions, 1,
		"the exemption list is size-pinned — widening it is how an undeclared field gets waved through")
	require.Contains(t, RecordDecisionToolDef().InputSchema.Properties, "alternatives",
		"the alternatives exemption must be EARNED: its owning tool has to declare it")

	cases := []struct {
		tool   string
		args   any
		schema map[string]kgtools.Property
	}{
		{"mutate", mutateArgs{}, mutateProperties()},
		{"thoughts", thinkArgs{}, ThoughtsToolDef().InputSchema.Properties},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			for field := range reflect.TypeOf(tc.args).Fields() {
				key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
				if key == "" || key == "-" {
					// No json tag: the unexported raw-payload carrier on mutateArgs is
					// the only such field, and it never crosses the wire.
					continue
				}
				if reason, exempt := exemptions[key]; exempt {
					t.Logf("%s wire field %q exempt: %s", tc.tool, key, reason)
					continue
				}
				// Membership is checked explicitly rather than with assert.Contains
				// on the map: a Contains failure renders the WHOLE schema map into
				// the failure text, burying the one key that matters.
				_, declared := tc.schema[key]
				assert.Truef(t, declared,
					"%s wire field %q is consumed off the wire but the %s schema does not declare it — "+
						"a schema-invisible param is one callers cannot discover and the accounting gate cannot reject",
					tc.tool, key, tc.tool)
			}
		})
	}
}

// TestNegationProofParams_RejectionNamesTopLevel pins that the rejection says
// WHERE to supply the quote, not merely WHAT to supply. Two independent
// measurements of one claim: the const check catches a reworded message, and the
// driven check catches a message that is never actually reached.
func TestNegationProofParams_RejectionNamesTopLevel(t *testing.T) {
	const wantLocation = "as a TOP-LEVEL param on this call"

	t.Run("the locked message names the location", func(t *testing.T) {
		assert.Contains(t, errFirstPartyEvidenceMsg, wantLocation,
			"the rejection must tell the negator WHERE the quote goes — naming only WHAT to "+
				"supply is what sent the reporter to metadata and edge_evidence")
	})

	t.Run("a driven rejection carries the location", func(t *testing.T) {
		const nodeID = "th-target"
		deps := interceptTestDeps{gc: &citedSourceFake{
			thoughtNode: &knowledgev1.Node{
				Id: nodeID, Type: string(kgtypes.NodeThought),
				Summary: "the contradicted claim", Content: "the live reasoning body of the node",
			},
		}}
		handled, res := InterceptNegationGate(opCtx(), deps, negationParams(t, "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
		}))
		require.True(t, handled, "an unquoted negation is claimed by the gate")
		require.True(t, res.IsError, "and rejected: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), wantLocation,
			"the message the caller actually receives must carry the location")
	})
}

// TestNegationProofParams_NonNegationCallRejected pins the per-CALL refinement
// the per-ARM registry cannot express: negation-ness is decided by the
// relationship/status VALUE, so a link arm that consumes verified_quote would
// otherwise accept-and-ignore it on relationship:"relates-to" — the silent-drop
// shape this gate exists to close.
//
// Every subtest runs with a NIL GraphCaller on purpose: the check is pure arg
// shape and reads no ground truth, so it must stay loud in the daemon's
// degraded mode rather than riding the fail-open.
//
// The last two subtests are CHARACTERIZATION GUARDS — green before AND after
// this change — present to prove the new check does not over-fire. Claiming
// them as red-first would be false.
func TestNegationProofParams_NonNegationCallRejected(t *testing.T) {
	nilGraph := interceptTestDeps{gc: nil}

	t.Run("relates-to link carrying verified_quote rejects", func(t *testing.T) {
		handled, res := InterceptNegationGate(opCtx(), nilGraph, negationParams(t, "mutate", map[string]any{
			"operation": "link", "relationship": "relates-to", "from": "a", "to": "b",
			"verified_quote": "some quote",
		}))
		require.True(t, handled, "a proof param on a non-negation call must be CLAIMED, not fall through")
		require.True(t, res.IsError, "and rejected: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "verified_quote", "the rejection must NAME the param")
	})

	t.Run("think without branches_from carrying cited_range rejects", func(t *testing.T) {
		handled, res := InterceptNegationGate(opCtx(), nilGraph, negationParams(t, "thoughts", map[string]any{
			"operation": "think", "content": "c", "summary": "s",
			"cited_range": "pkg/file.go:1-3",
		}))
		require.True(t, handled, "a proof param on a non-supersession think must be CLAIMED")
		require.True(t, res.IsError, "and rejected: %s", toolResultText(res))
		assert.Contains(t, toolResultText(res), "cited_range", "the rejection must NAME the param")
	})

	t.Run("characterization guard: a clean non-negation link falls through", func(t *testing.T) {
		handled, res := InterceptNegationGate(opCtx(), nilGraph, negationParams(t, "mutate", map[string]any{
			"operation": "link", "relationship": "relates-to", "from": "a", "to": "b",
		}))
		assert.False(t, handled, "a non-negation call carrying NO proof param is untouched by the gate")
		assert.False(t, res.IsError, "and must not be rejected: %s", toolResultText(res))
	})

	t.Run("characterization guard: nil graph still fails open on a real negation", func(t *testing.T) {
		handled, res := InterceptNegationGate(opCtx(), nilGraph, negationParams(t, "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": "th-1",
			"verified_quote": "some quote",
		}))
		assert.False(t, handled,
			"the documented fail-open survives: with no graph access the gate cannot read ground "+
				"truth, so a real negation falls through to the existing handler")
		assert.False(t, res.IsError, "fail-open is a fall-through, never an error: %s", toolResultText(res))
	})
}
