// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_prefix_test.go asserts the ONE thing every practice-hub refusal
// owes its reader before it says anything else: which arm it is about, said once.
//
// WHAT THE AUDIT FOUND. The head gate's refusals reached the caller as
// "mutate(create): mutate(create): `source_hub`=…", because the rules moved
// beneath the tools layer and kept the arm prefix the engine now writes, while
// the tools-layer call site still added its own. Nothing was mis-refused; the
// message just said it twice, which is the tell of two layers each believing it
// owns the framing.
//
// WHY THE COUNT AND NOT THE SHAPE. Asserting the exact rendered string pins the
// prose, which is edited every round. Asserting that the arm token appears
// EXACTLY ONCE pins the property: not zero, so a caller is never left guessing
// which call earned the refusal, and not two, whoever adds the next layer.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestPracticeHubRefusals_NameTheirArmExactlyOnce(t *testing.T) {
	for _, row := range []struct{ name, op, payload string }{
		{"the membership relation on a link", "link",
			`{"operation":"link","graph":"practice","from":"` + hubTargetMemberA + `","to":"` +
				hubEndpointsHubA + `","relationship":"` + string(kgtypes.EdgeSourcedFrom) + `"}`},
		{"an empty endpoint on a hub-scoped link", "link",
			`{"operation":"link","graph":"practice","source_hub":"` + hubEndpointsHubA +
				`","from":"","to":"` + hubTargetMemberB + `","relationship":"relates-to"}`},
		{"a hub created under another hub", "create",
			`{"operation":"create","graph":"practice","source_hub":"` + hubEndpointsHubA +
				`","type":"` + string(kgtypes.NodeSource) + `","name":"H","summary":"s"}`},
		{"a body naming a different hub than the call", "create",
			`{"operation":"create","graph":"practice","source_hub":"` + hubEndpointsHubA +
				`","type":"pattern","name":"n","summary":"s","metadata":{"` +
				kgtypes.MetaKeySourceHub + `":"` + hubEndpointsHubB + `"}}`},
		{"a hub-scoped update naming no target", "update",
			`{"operation":"update","graph":"practice","source_hub":"` + hubEndpointsHubA +
				`","status":"active"}`},
		{"a body carrying a hub the call did not name", "create",
			`{"operation":"create","graph":"practice","type":"pattern","name":"n","summary":"s",` +
				`"metadata":{"` + kgtypes.MetaKeySourceHub + `":"` + hubGhost + `"}}`},
	} {
		t.Run(row.name, func(t *testing.T) {
			fc := practiceHubTargetFake(t)
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate", Arguments: json.RawMessage(row.payload),
			})
			require.True(t, handled, "the intercept claims a practice write it refuses")
			require.True(t, res.IsError, "the row must REFUSE, or it asserts nothing about a refusal")
			require.NotEmpty(t, res.Content)

			body := res.Content[0].Text
			token := "mutate(" + row.op + "): "
			assert.Equal(t, 1, strings.Count(body, token),
				"the refusal names its arm exactly once — zero leaves the caller guessing which call "+
					"earned it, and two is a second layer framing a message that already had one.\n%s", body)
			assert.Empty(t, fc.execMutations, "and nothing is written")
		})
	}
}
