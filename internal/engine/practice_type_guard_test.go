// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// practice_type_guard_test.go drives the closed-vocabulary rule through the REAL
// exported entry point every practice write passes, with the arms that must not
// fire beside the ones that must.
//
// exec IS nil AND IS NEVER REACHED. The rule is payload-decidable and runs ahead
// of the one rule that reads, and none of these payloads names a hub, so the
// reading rule has nothing to resolve. A nil caller is therefore the honest
// fixture: if a future edit moved this rule below the read, these rows would
// panic rather than quietly passing on a fake's canned answer.

// unenrolledType is a node type the vocabulary does not enroll, spelled as a
// literal so the test states its input rather than asking the vocabulary under
// test to produce one.
const unenrolledType = "widget_of_nowhere"

// guardWrite runs the engine-layer rules over a raw payload.
func guardWrite(t *testing.T, payload string) error {
	t.Helper()
	return GuardPracticeWrite(context.Background(), nil, json.RawMessage(payload))
}

func TestGuardPracticeWrite_UnenrolledTypeIsRefusedOnEveryCreateArm(t *testing.T) {
	for _, tc := range []struct {
		name, payload, arm, role string
	}{
		{
			name: "create",
			payload: `{"operation":"create","graph":"practice","type":"` + unenrolledType +
				`","name":"W","summary":"s"}`,
			arm: "mutate(create)", role: "`type`",
		},
		{
			name: "create_batch with one bad body among enrolled ones",
			payload: `{"operation":"create_batch","graph":"practice","nodes":[` +
				`{"type":"pattern","name":"P","summary":"s"},` +
				`{"type":"` + unenrolledType + `","name":"W","summary":"s"},` +
				`{"type":"idiom","name":"I","summary":"s"}]}`,
			arm: "mutate(create_batch)", role: "`nodes[1].type`",
		},
		{
			name: "upsert",
			payload: `{"operation":"upsert","graph":"practice","id":"u1","type":"` + unenrolledType +
				`","name":"W","summary":"s"}`,
			arm: "mutate(upsert)", role: "`type`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := guardWrite(t, tc.payload)
			require.Error(t, err, "the practice graph enrolls no such type")
			assert.Contains(t, err.Error(), tc.arm, "the refusal names the arm the caller used")
			assert.Contains(t, err.Error(), tc.role, "and the payload path that carried the type")
			assert.Contains(t, err.Error(), unenrolledType, "and the offending type")
			assert.Contains(t, err.Error(), "practice graph", "and the graph that does not enroll it")
			assert.Contains(t, err.Error(), "pattern", "and the admitted vocabulary, so the repair is in the message")
			assert.Contains(t, err.Error(), "nothing was written", "on the same terms as every refusal here")
		})
	}
}

// TestGuardPracticeWrite_EnrolledTypesAreAdmitted is the near-miss control: the
// SAME payloads with an enrolled type pass. Every practice type the live graph
// carries is exercised, because a rule that admitted only the two types a
// refusal message happens to name would satisfy the rows above and break every
// landing.
func TestGuardPracticeWrite_EnrolledTypesAreAdmitted(t *testing.T) {
	for _, typ := range []kgtypes.NodeType{
		kgtypes.NodePattern, kgtypes.NodeIdiom, kgtypes.NodeUseCase, kgtypes.NodeSource,
		kgtypes.NodeExample, kgtypes.NodeReference, kgtypes.NodeFinding, kgtypes.NodeDocument,
	} {
		t.Run(string(typ), func(t *testing.T) {
			require.NoError(t, guardWrite(t,
				`{"operation":"create","graph":"practice","type":"`+string(typ)+`","name":"N","summary":"s"}`),
				"an enrolled practice type must still write")
			require.NoError(t, guardWrite(t,
				`{"operation":"create_batch","graph":"practice","nodes":[{"type":"`+string(typ)+
					`","name":"N","summary":"s"}]}`))
		})
	}
}

// TestGuardPracticeWrite_ArmsThatMustNotFire pins the rule's boundaries: the
// operations that carry no create body, and every graph but practice.
//
// EACH ROW IS A WAY AN OVER-BROAD RULE WOULD SHOW. An update names a node that
// already exists and does not set its type, so refusing it on a type it does not
// carry would refuse repairs to rows already in the graph; and a knowledge-graph
// create of an undocumented type is a shape the validator deliberately admits.
func TestGuardPracticeWrite_ArmsThatMustNotFire(t *testing.T) {
	t.Run("the update-shaped arms carry no create body", func(t *testing.T) {
		for _, op := range []string{"update", "update_batch", "bulk_update_metadata"} {
			t.Run(op, func(t *testing.T) {
				require.NoError(t, guardWrite(t,
					`{"operation":"`+op+`","graph":"practice","id":"n1","type":"`+unenrolledType+
						`","items":[{"id":"n1","type":"`+unenrolledType+`"}],"updates":[{"id":"n1","type":"`+
						unenrolledType+`"}]}`),
					"%s does not create a node, so its payload's type decides nothing", op)
			})
		}
	})

	t.Run("link, unlink and delete carry no type at all", func(t *testing.T) {
		for _, op := range []string{"link", "unlink", "delete"} {
			require.NoError(t, guardWrite(t,
				`{"operation":"`+op+`","graph":"practice","from":"a","to":"b","relationship":"relates-to"}`),
				"%s is not a create-shaped arm", op)
		}
	})

	t.Run("every other family is untouched", func(t *testing.T) {
		for _, graph := range []string{"", "knowledge", "code", "checks", "linkage", "widgets"} {
			t.Run("graph="+graph, func(t *testing.T) {
				require.NoError(t, guardWrite(t,
					`{"operation":"create","graph":"`+graph+`","type":"`+unenrolledType+`","name":"W","summary":"s"}`),
					"the rule is the PRACTICE graph's closed vocabulary, not a global one")
			})
		}
	})

	t.Run("a create with no type is left to the required-field rule", func(t *testing.T) {
		require.NoError(t, guardWrite(t, `{"operation":"create","graph":"practice","name":"W","summary":"s"}`),
			"an empty type is a missing field, and a vocabulary lecture is the wrong message for one")
	})
}

// TestPracticeUnenrolledTypeRefusal_NamesTheVocabularyStably pins the one
// property of the message a Go map cannot supply on its own: the enrolled set is
// rendered in a stable order. A refusal that named its vocabulary differently on
// every run is one no gate can assert — the same reason the recipe validator
// sorts its violations.
func TestPracticeUnenrolledTypeRefusal_NamesTheVocabularyStably(t *testing.T) {
	first := PracticeUnenrolledTypeRefusal("type", unenrolledType).Error()
	require.Contains(t, first, "pattern")
	require.Contains(t, first, "idiom")
	for range 20 {
		require.Equal(t, first, PracticeUnenrolledTypeRefusal("type", unenrolledType).Error())
	}

	// EVERY OFFERED REPAIR MUST SURVIVE THE SERVER'S NEXT RULE. These five are
	// system-managed: the server refuses a hand-written create of them one rule
	// after the vocabulary check, so offering them here would send an author who
	// followed the message to a second refusal. Each is asserted by name so a
	// failure says which one leaked, and each is asserted to be ENROLLED still —
	// an exclusion that dropped it from the vocabulary would ADMIT the write
	// rather than refuse it, which is the opposite mistake.
	for _, blocked := range []kgtypes.NodeType{
		kgtypes.NodeFile, kgtypes.NodePackage, kgtypes.NodeBranch,
		kgtypes.NodeGithubRepo, kgtypes.NodeProxy,
	} {
		require.NotContains(t, first, string(blocked),
			"%q is system-managed — a refusal must not offer it as a repair", blocked)
		require.True(t, blocked.IsEnrolled(),
			"%q must still be enrolled: the message drops it, the RULE does not", blocked)
	}
}
