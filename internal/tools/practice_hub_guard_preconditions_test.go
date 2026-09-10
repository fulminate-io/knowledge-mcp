// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_guard_preconditions_test.go closes three arms of the practice hub
// guards that NO test observed, found by deleting each one and watching the whole
// client module stay green.
//
// HOW THEY WERE FOUND, AND WHY THEY MATTER. Every guard this ticket added was
// re-mutated on this commit: the arm was removed and the suite re-run. Ten of the
// fourteen turned a named test red. Three did not, and an arm whose absence
// changes nothing is not a guard — it is a comment that compiles. Each is closed
// here rather than reported and left, because a reader cannot tell an
// unobserved-but-correct arm from one that was already broken.
//
// THE THREE, AND THEIR DIFFERENT REACHES:
//
//   - THE NO-TARGET REFUSAL is reachable by an ordinary caller and simply had no
//     row: a hub-scoped update, update_batch or bulk_update_metadata naming no id
//     at all. It is driven through the real intercept below.
//   - THE FAMILY SELF-FILTER on both arm-level guards is now a PRECONDITION
//     rather than a decision, because refusePracticeHubOffFamily refuses every
//     foreign family at the head of the dispatch. No payload can reach these
//     guards off the practice family, so the filter is unreachable from
//     InterceptMutate and is asserted by calling the guards directly.
//   - THE NIL-CALLER REFUSAL is unreachable for a different reason: InterceptMutate
//     returns before every guard when deps.GraphCaller() is nil. It is kept, and
//     asserted directly, because a guard that cannot see its targets has not
//     checked them, and the next caller wiring these guards in must inherit that
//     answer rather than a nil-pointer dereference.
//
// A DIRECT CALL IS THE HONEST INSTRUMENT FOR THE LAST TWO, and the reason is
// stated rather than assumed: driving them through InterceptMutate would assert
// against a path that cannot reach the arm, which is how an unobserved guard
// stays unobserved while looking tested.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestPracticeTargetHub_NoTargetIsRefused is the missing row: the hub scopes the
// targets, so a call that names none has given the scope nothing to apply.
//
// SERVING IT WOULD BE THE WIDEST POSSIBLE READING of a narrowing param — the
// caller asked for "within this hub" and would get "whatever this payload
// otherwise selects" — so it is refused, and the refusal names the operation the
// caller has to add a target to.
func TestPracticeTargetHub_NoTargetIsRefused(t *testing.T) {
	for _, op := range []string{"update", "update_batch", "bulk_update_metadata"} {
		t.Run(op, func(t *testing.T) {
			fc := practiceHubTargetFake(t)
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name:      "mutate",
				Arguments: json.RawMessage(hubTargetPayload(op, hubEndpointsHubA, "")),
			})
			require.Truef(t, handled, "a %s naming no target is CLAIMED, never declined to a path that ignores the hub", op)
			require.Truef(t, res.IsError, "and refused: %s", toolResultText(res))
			body := toolResultText(res)
			assert.Contains(t, body, hubEndpointsHubA, "the refusal names the hub the caller scoped to")
			assert.Containsf(t, body, "names no target", "and says what is missing: %s", body)
			assert.Contains(t, body, op, "and names the operation, so the caller knows which key to add")
			assert.Empty(t, fc.execMutations, "nothing may be written")
			assert.Empty(t, hubEndpointReads(fc),
				"and no read is paid: the fault is decidable from the payload alone")
		})

		// SAME-RUN KNOWN POSITIVE: the identical call WITH a member target serves.
		// Without it every row above would be equally satisfied by an arm that
		// refused every hub-scoped call of this shape.
		t.Run(op+"/control: one member target serves", func(t *testing.T) {
			_, _, res := driveHubTarget(t,
				hubTargetPayload(op, hubEndpointsHubA, hubTargetsFor(op, hubTargetMemberA)))
			assert.Falsef(t, res.IsError, "%s must serve when it names a member: %s", op, toolResultText(res))
		})
	}
}

// TestPracticeHubGuards_SelfFilterOnTheFamily pins the precondition both
// arm-level guards now rest on: off the practice family they decide NOTHING and
// pay no read, because the head gate has already refused that call.
//
// IT IS A PRECONDITION WORTH PINNING rather than dead weight. Both guards are
// called from arms that carry many families, so a guard that resolved ids in the
// combined practice graph for a code-graph payload would refuse a foreign write
// for not being under a hub it never claimed to be under — a wrong refusal, paid
// for with a wrong read.
func TestPracticeHubGuards_SelfFilterOnTheFamily(t *testing.T) {
	for _, family := range []string{string(kgtypes.GraphKnowledge), string(kgtypes.GraphCode), checksGraphSelector} {
		t.Run(family, func(t *testing.T) {
			fc := practiceHubTargetFake(t)
			targets := mutateArgs{
				Operation: "update", Graph: family, SourceHub: hubEndpointsHubA, ID: hubTargetOtherHub,
			}
			assert.NoError(t, guardPracticeHubScopesTargets(opCtx(), fc, targets),
				"the target guard must stand aside off the practice family")

			endpoints := mutateArgs{
				Operation: "unlink", Graph: family, SourceHub: hubEndpointsHubA,
				From: hubTargetOtherHub, To: hubTargetMemberB,
			}
			assert.NoError(t, guardPracticeHubScopesEndpoints(opCtx(), fc, endpoints),
				"the endpoint guard must stand aside off the practice family")
			assert.Empty(t, hubEndpointReads(fc),
				"and neither may pay a practice-graph read for a call that is not a practice call")
		})
	}

	// THE KNOWN POSITIVE, on the same instrument and the same ids: on PRACTICE the
	// identical args refuse, because hubTargetOtherHub is grouped under a different
	// hub. Without this pair the nil returns above would be satisfied by guards
	// that never refuse anything.
	t.Run("control: the same args on practice refuse", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		practice := string(kgtypes.GraphPractice)
		require.Error(t, guardPracticeHubScopesTargets(opCtx(), fc, mutateArgs{
			Operation: "update", Graph: practice, SourceHub: hubEndpointsHubA, ID: hubTargetOtherHub,
		}), "the target guard must refuse a target under another hub")
		require.Error(t, guardPracticeHubScopesEndpoints(opCtx(), fc, mutateArgs{
			Operation: "unlink", Graph: practice, SourceHub: hubEndpointsHubA,
			From: hubTargetOtherHub, To: hubTargetMemberB,
		}), "the endpoint guard must refuse an endpoint under another hub")
		assert.NotEmpty(t, hubEndpointReads(fc), "and both must have actually read to decide")
	})
}

// TestPracticeHubGuards_UnreadableCallerRefuses pins the answer the two hub
// guards and the upsert membership read give when they have no graph to read.
//
// THE ARM IS UNREACHABLE FROM InterceptMutate TODAY, which is exactly why each
// reader needs its own row here: the dispatch returns before every guard when the
// client has no GraphCaller, so nothing in the shipped path can turn these arms
// red, and a sweep over the shipped path alone reports them as silent. It is
// kept rather than deleted because the alternative to refusing is a guard that
// silently approves every target it could not see, and the next caller to wire
// these guards somewhere else must inherit the refusal rather than discover a
// nil-pointer dereference.
func TestPracticeHubGuards_UnreadableCallerRefuses(t *testing.T) {
	practice := string(kgtypes.GraphPractice)

	targetsErr := guardPracticeHubScopesTargets(opCtx(), nil, mutateArgs{
		Operation: "update", Graph: practice, SourceHub: hubEndpointsHubA, ID: hubTargetMemberA,
	})
	require.Error(t, targetsErr, "a guard with no graph to read has checked nothing and must say so")
	assert.Contains(t, targetsErr.Error(), "could not be read")
	assert.Contains(t, targetsErr.Error(), hubTargetMemberA, "and names the target it could not check")

	endpointsErr := guardPracticeHubScopesEndpoints(opCtx(), nil, mutateArgs{
		Operation: "unlink", Graph: practice, SourceHub: hubEndpointsHubA,
		From: hubTargetMemberA, To: hubTargetMemberB,
	})
	require.Error(t, endpointsErr, "the endpoint guard owes the same answer")
	assert.Contains(t, endpointsErr.Error(), "could not be read")

	// THE CARRY-FORWARD OWES IT TOO, and for a sharper reason than the two above:
	// its "" return means "grouped under no hub", so a nil caller answered with ""
	// would not merely skip a check, it would report a membership the reader does
	// not have and let the upsert write the body unstamped.
	_, carryErr := storedPracticeHubOf(opCtx(), nil, mutateArgs{}, hubTargetMemberA)
	require.Error(t, carryErr, "a membership that could not be read is not a membership of none")
	assert.Contains(t, carryErr.Error(), "could not be read")
	assert.Contains(t, carryErr.Error(), kgtypes.MetaKeySourceHub,
		"and names the key the refusal is protecting")

	// KNOWN POSITIVE: with a caller present the same args serve, so the refusals
	// above are about the missing caller and not about the payload.
	fc := practiceHubTargetFake(t)
	assert.NoError(t, guardPracticeHubScopesTargets(opCtx(), fc, mutateArgs{
		Operation: "update", Graph: practice, SourceHub: hubEndpointsHubA, ID: hubTargetMemberA,
	}))
	assert.NoError(t, guardPracticeHubScopesEndpoints(opCtx(), fc, mutateArgs{
		Operation: "unlink", Graph: practice, SourceHub: hubEndpointsHubA,
		From: hubTargetMemberA, To: hubTargetMemberB,
	}))
	carried, carriedErr := storedPracticeHubOf(opCtx(), fc, mutateArgs{}, hubTargetMemberA)
	require.NoError(t, carriedErr)
	assert.Equal(t, hubEndpointsHubA, carried, "and it reads the member's actual hub")
}
