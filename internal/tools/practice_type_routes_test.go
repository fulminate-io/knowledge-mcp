// SPDX-License-Identifier: Apache-2.0

package tools

// practice_type_routes_test.go drives the practice graph's closed-vocabulary
// refusal down BOTH routes a practice write can take, for the reason the hub
// rules' own bypass file records: a gate added per caller is a gate the next
// caller bypasses.
//
// THE TWO ROUTES. The recipe landing and the style-rule import compose a
// create_batch and send it through executeMutate, the tools' compile-and-execute
// funnel, which never reaches InterceptMutate; the mutate tool reaches
// engine.Dispatch through the intercept. A rule that lived in only one of them
// would be green here and absent in production for whichever caller took the
// other.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// unenrolledEmitType is a node type the vocabulary does not enroll — the shape a
// recipe author reaches for when they invent a type for their own collection.
const unenrolledEmitType = "widget_of_nowhere"

// landingBatchOfType renders the landing's own batch shape (a hub keyed to
// itself, a member carrying the hub id, and the member's `sourced-from` edge)
// with the MEMBER's type as the variable. Everything else is the shape the
// shipped landing writes, so a refusal can only be the member's type.
func landingBatchOfType(memberType string) string {
	const hubID = "typeroute-hub-1"
	return `{"operation":"create_batch","graph":"practice","nodes":[` +
		`{"type":"` + string(kgtypes.NodeSource) + `","id":"` + hubID +
		`","name":"h","summary":"s","metadata":{"` + kgtypes.MetaKeySourceHub + `":"` + hubID + `"}},` +
		`{"type":"` + memberType + `","id":"typeroute-member-1","name":"m","summary":"s","metadata":{"` +
		kgtypes.MetaKeySourceHub + `":"` + hubID + `"}}` +
		`],"edges":[{"from_idx":1,"to_idx":0,"type":"` + string(kgtypes.EdgeSourcedFrom) + `"}]}`
}

func TestPracticeUnenrolledType_RefusedOnTheLandingRoute(t *testing.T) {
	t.Run("an unenrolled member type refuses the batch and writes nothing", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		_, err := executeMutate(opCtx(), fc, json.RawMessage(landingBatchOfType(unenrolledEmitType)))
		require.Error(t, err, "the landing route runs the vocabulary rule")
		assert.Contains(t, err.Error(), unenrolledEmitType, "the refusal names the offending type")
		assert.Contains(t, err.Error(), "practice graph", "and the graph that does not enroll it")
		assert.Empty(t, fc.execMutations,
			"and NOTHING is written — not the member, and not the well-formed hub ahead of it in the batch")
	})

	t.Run("the near-miss control: an ENROLLED member type lands", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		_, err := executeMutate(opCtx(), fc, json.RawMessage(landingBatchOfType(string(kgtypes.NodeIdiom))))
		require.NoError(t, err, "the only difference from the refused batch is the member's type: %v", err)
		assert.Len(t, fc.execMutations, 1, "and it is ONE create_batch")
	})
}

func TestPracticeUnenrolledType_RefusedOnTheInterceptRoute(t *testing.T) {
	t.Run("a mutate(create) of an unenrolled type is refused", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		res, err := engine.Dispatch(opCtx(), fc.Execute, fc.Stats, "mutate", json.RawMessage(
			`{"operation":"create","graph":"practice","type":"`+unenrolledEmitType+`","name":"W","summary":"s"}`))
		require.NoError(t, err)
		require.True(t, res.IsError, "the intercept route runs the same rule")
		body := toolResultText(res)
		assert.Contains(t, body, unenrolledEmitType)
		assert.Contains(t, body, "nothing was written")
		assert.Empty(t, fc.execMutations, "nothing reaches the wire")
	})

	t.Run("the near-miss control: an ENROLLED type writes", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		res, err := engine.Dispatch(opCtx(), fc.Execute, fc.Stats, "mutate", json.RawMessage(
			`{"operation":"create","graph":"practice","type":"`+string(kgtypes.NodePattern)+
				`","name":"P","summary":"s"}`))
		require.NoError(t, err)
		require.False(t, res.IsError, toolResultText(res))
		assert.NotEmpty(t, fc.execMutations, "and it writes")
	})
}
