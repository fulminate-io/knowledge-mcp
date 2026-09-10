// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_resolve_test.go drives the HUB ITSELF, which every earlier round's
// gate took on trust.
//
// WHAT THE EARLIER GATES ASSERT, AND WHAT THEY DO NOT. Five rounds of hub work
// each guarded the PARAMETER: which arms take it, which targets it scopes, which
// families may name it, which bodies may repeat it, and that an upsert cannot
// drop it. Every one of them asks what the caller may do with a hub id. None of
// them asks whether the id names a hub. A `source_hub` that resolves to no node
// was accepted on every arm, and the write landed a `sourced-from` edge pointing
// at nothing: the metadata carrier then reports the node as a member while a
// traverse from it reaches no hub and a traverse from the hub cannot resolve its
// own root.
//
// SO THE ROWS BELOW ARE ABOUT THE NODE. A hub id must name a LIVE node of type
// source in the combined practice graph, and the four ways it can fail — absent,
// tombstoned, some other type, or a member id used as a hub — are four refusals
// naming the hub and the arm.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The hub states the resolver has to tell apart. hubGhost is seeded NOWHERE, which
// is the shape the review found accepted on every arm.
const (
	hubGhost     = "ffffffffffffffffffffffffffffffff"
	hubTombstone = "cccccccccccccccccccccccccccccccc"
	// hubNestedHub is a `source` node that is ITSELF grouped under another hub.
	// It is a live shape rather than a hypothetical: a create of type source
	// carrying a source_hub produced exactly this node before the nesting rule.
	hubNestedHub = "dddddddddddddddddddddddddddddddd"
)

// hubResolvePayload renders one arm's call carrying a hub, from the same
// fragments the sibling files drive, so a row here differs from its counterpart
// there in the HUB alone.
func hubResolvePayload(operation, hub string) string {
	switch operation {
	case "create":
		return `{"operation":"create","graph":"practice","source_hub":"` + hub +
			`","type":"pattern","name":"n","summary":"s"}`
	case "create_batch":
		return `{"operation":"create_batch","graph":"practice","source_hub":"` + hub +
			`","nodes":[{"type":"pattern","name":"n","summary":"s"}]}`
	case "delete":
		return `{"operation":"delete","graph":"practice","source_hub":"` + hub + `"}`
	case "link", "unlink":
		return `{"operation":"` + operation + `","graph":"practice","source_hub":"` + hub +
			`","from":"` + hubTargetMemberA + `","to":"` + hubTargetMemberB + `","relationship":"contains"}`
	case "answer":
		return `{"operation":"answer","graph":"practice","source_hub":"` + hub +
			`","id":"` + hubTargetMemberA + `","conclusion":"probe","summary":"probe"}`
	case "update", "update_batch", "bulk_update_metadata", "upsert":
		return hubTargetPayload(operation, hub, hubTargetsFor(operation, hubTargetMemberA))
	}
	// NO DEFAULT, and that is the point. A builder that fell through to a generic
	// shape would hand the census a payload for an operation nobody wrote a row
	// for, and the census would then report an undriven arm as driven. An empty
	// string is the honest answer, and TestPracticeHubArms_EveryDeclaredArmIsDrivenOrExcused
	// fails by name on it.
	return ""
}

// hubNoWriteArms names the declared operations that reach NO practice write for a
// hub to reach. It is the ONE hand-written half of the arm census, and it is
// deliberately small: every operation not in it is driven with a hub.
//
// `answer` is the only member. On a foreign graph it is denied and compiles to
// nothing, so there is no plan for the hub to ride and no carrier for the
// invariant to check. TestPracticeHubArms_EveryDeclaredArmIsDrivenOrExcused
// asserts that reason rather than trusting this list.
func hubNoWriteArms() map[string]bool {
	return map[string]bool{"answer": true}
}

// hubResolveArms is the population every row below iterates: every practice write
// arm that takes a hub. It is DERIVED from the schema's own operation vocabulary
// (mutateDeclaredOperations) minus the no-write arms above, so an operation added
// to the schema lands here with no row and fails
// TestPracticeHubArms_EveryDeclaredArmIsDrivenOrExcused by name.
func hubResolveArms() []string {
	excused := hubNoWriteArms()
	out := make([]string, 0, len(mutateDeclaredOperations))
	for _, op := range mutateDeclaredOperations {
		if !excused[op] {
			out = append(out, op)
		}
	}
	return out
}

// TestPracticeHubArms_EveryDeclaredArmIsDrivenOrExcused is the arm census, and it
// replaces a comment that PROMISED it.
//
// WHY IT IS DERIVED RATHER THAN LISTED. Every earlier round closed a hub gate and
// enumerated the arms it applied to by hand, and the review that followed found
// an arm the list had missed — the edge arms writing the membership relation were
// the fourth. A hand-written population cannot fail for the arm nobody thought
// of. This one reads the schema's own declared vocabulary, so an operation added
// to the mutate surface is either DRIVEN here with a hub, through the same node
// invariant every other row ends with, or named in the small excused set with the
// reason asserted; an operation with neither fails by name.
func TestPracticeHubArms_EveryDeclaredArmIsDrivenOrExcused(t *testing.T) {
	require.NotEmpty(t, mutateDeclaredOperations,
		"the mutate schema declares no operations, so this census would be vacuous")

	driven := map[string]bool{}
	for _, op := range hubResolveArms() {
		driven[op] = true
	}
	excused := hubNoWriteArms()

	for _, op := range mutateDeclaredOperations {
		t.Run(op, func(t *testing.T) {
			require.Truef(t, driven[op] || excused[op],
				"mutate operation %q is declared by the schema and this census neither DRIVES it with a hub "+
					"nor excuses it — an arm nobody listed is the hole every earlier round was found through",
				op)
			payload := hubResolvePayload(op, hubEndpointsHubA)
			require.NotEmptyf(t, payload, "operation %q has no payload to drive it with", op)

			fc, _, res := driveHubTarget(t, payload)
			if excused[op] {
				// THE EXCUSE IS ASSERTED, not taken on trust: the arm must reach no
				// write at all, which is what makes "no carrier to check" true.
				assert.Emptyf(t, fc.execMutations, "%q is excused as reaching no write, so it must write nothing", op)
				_, compiles := engine.Compile("mutate", json.RawMessage(payload))
				assert.Falsef(t, compiles, "%q is excused as reaching no write, so its payload must not compile", op)
				return
			}
			// DRIVEN: the drive helper runs the node invariant on whatever plan
			// this arm would apply, so an arm that could split the two carriers
			// fails here rather than in the next review.
			assert.Falsef(t, res.IsError, "%q under a real hub must serve: %s", op, toolResultText(res))
		})
	}

	// The excused set is held to the vocabulary too: a name in it that the schema
	// does not declare excuses nothing and would hide an arm behind a typo.
	for op := range excused {
		assert.Containsf(t, mutateDeclaredOperations, op,
			"the excused set names %q, which the mutate schema does not declare", op)
	}
}

// hubIsTheOnlyFault reports whether an arm's refusal for a bad hub can only be
// about the hub. create, create_batch and delete name no other id for a sibling
// rule to object to; every other arm scopes targets or endpoints, and a hub that
// resolves to nothing makes those fail first.
func hubIsTheOnlyFault(op string) bool {
	switch op {
	case "create", "create_batch", "delete":
		return true
	}
	return false
}

// TestPracticeHubResolve_AHubMustNameALiveSourceNode is the decided contract: the
// hub id is resolved before any arm acts on it.
func TestPracticeHubResolve_AHubMustNameALiveSourceNode(t *testing.T) {
	for _, row := range []struct{ name, hub, want string }{
		{"a hub that resolves to no node", hubGhost, "resolves to no node"},
		{"a hub whose node is DELETED", hubTombstone, "has been deleted"},
		{"a member id used as a hub", hubTargetMemberA, "is a `pattern`"},
	} {
		for _, op := range hubResolveArms() {
			t.Run(row.name+"/"+op, func(t *testing.T) {
				fc, _, res := driveHubTarget(t, hubResolvePayload(op, row.hub))
				require.Truef(t, res.IsError, "%s must refuse: %s", op, toolResultText(res))

				body := toolResultText(res)
				assert.Contains(t, body, row.hub, "the refusal names the hub the caller typed")
				assert.Containsf(t, body, op, "and names the arm it refused (%s)", op)
				assert.Empty(t, fc.execMutations, "nothing is written")

				// THE SENTENCE IS ASSERTED WHERE THE HUB IS THE ONLY FAULT. On the
				// arms that ALSO scope targets or endpoints, a hub that names
				// nothing makes every target "under another hub", so that rule
				// speaks first — the call is refused either way, and demanding the
				// hub's own sentence there would be asserting an ordering rather
				// than a rule.
				if hubIsTheOnlyFault(op) {
					assert.Contains(t, body, row.want, "and says what is wrong with the hub")
				}
			})
		}
	}

	t.Run("the control: a real hub serves on every arm", func(t *testing.T) {
		// The same-run KNOWN POSITIVE for all of the above. Without it every row
		// could be red for a reason unrelated to the hub.
		for _, op := range hubResolveArms() {
			_, _, res := driveHubTarget(t, hubResolvePayload(op, hubEndpointsHubA))
			assert.Falsef(t, res.IsError, "%s under a real hub must serve: %s", op, toolResultText(res))
		}
	})
}

// TestPracticeHubResolve_BodyCarriedHubsAreResolvedToo closes the other way a
// payload names a hub. A create_batch composed by a migration driver carries its
// hub in each `nodes[]` body, and a resolver that read only the call parameter
// would leave that route open — which is the route the whole body-carrier gate
// was written for one round earlier.
func TestPracticeHubResolve_BodyCarriedHubsAreResolvedToo(t *testing.T) {
	fc, handled, res := driveHubTarget(t,
		`{"operation":"create_batch","graph":"practice","source_hub":"`+hubGhost+
			`","nodes":[{"type":"pattern","name":"n","summary":"s","metadata":{"`+
			kgtypes.MetaKeySourceHub+`":"`+hubGhost+`"}}]}`)
	require.True(t, handled)
	require.True(t, res.IsError, toolResultText(res))
	assert.Contains(t, toolResultText(res), hubGhost)
	assert.Empty(t, fc.execMutations)

	// The control: the same batch under a real hub, named in BOTH carriers, still
	// serves — so the refusal above is about the hub's node, not about naming it twice.
	_, _, ok := driveHubTarget(t,
		`{"operation":"create_batch","graph":"practice","source_hub":"`+hubEndpointsHubA+
			`","nodes":[{"type":"pattern","name":"n","summary":"s","metadata":{"`+
			kgtypes.MetaKeySourceHub+`":"`+hubEndpointsHubA+`"}}]}`)
	assert.False(t, ok.IsError, toolResultText(ok))
}

// TestPracticeHubResolve_ReadCensus is the read census the hub rules owe, and it
// states the cost of the position they now hold rather than a budget they must
// meet.
//
// TWO RULES READ TWO DIFFERENT ID SETS. The hub's own validity — that the id
// names a live `source` node keyed to nothing but itself — is resolved in the
// ENGINE, beneath every caller, because two callers reach the engine without
// passing the intercept at all. The TARGET and ENDPOINT scoping is a different
// rule about a different set, and it stays in the intercept where the message
// names the payload path. So an arm that names targets pays two: one for the hub,
// one for its targets. The arms that name no other id pay exactly one.
func TestPracticeHubResolve_ReadCensus(t *testing.T) {
	for _, row := range []struct {
		op    string
		reads int
	}{
		{"create", 1}, {"create_batch", 1}, {"delete", 1},
		{"update", 2}, {"update_batch", 2}, {"bulk_update_metadata", 2},
		{"upsert", 2}, {"unlink", 2},
		// A practice LINK is CLAIMED by the intra-practice arm, which writes
		// without ever reaching the engine dispatch, so it pays the endpoint
		// scope's read alone. Its hub is checked by that same scoping: nothing is
		// grouped under an id that names no hub, so an endpoint under it cannot
		// resolve.
		{"link", 1},
	} {
		t.Run(row.op, func(t *testing.T) {
			fc, _, res := driveHubTarget(t, hubResolvePayload(row.op, hubEndpointsHubA))
			require.False(t, res.IsError, toolResultText(res))
			assert.Lenf(t, hubEndpointReads(fc), row.reads,
				"%s: the hub resolution and the target scope are two rules over two id sets", row.op)
		})
	}

	t.Run("a hub-less call still pays nothing on the arms that read nothing", func(t *testing.T) {
		// The other half of the census, and the reason the resolver self-filters on
		// the param: an omitted hub is the whole-graph reading and must stay free.
		for _, op := range []string{"update", "update_batch", "bulk_update_metadata"} {
			fc, _, res := driveHubTarget(t, hubTargetPayload(op, "", hubTargetsFor(op, hubTargetMemberA)))
			require.False(t, res.IsError, toolResultText(res))
			assert.Emptyf(t, hubEndpointReads(fc), "%s names no hub, so it buys no read", op)
		}
	})
}

// TestPracticeHubResolve_UnreadableHubRefuses: a resolver that could not read its
// hub has not checked it, on the same rule the sibling guards state.
func TestPracticeHubResolve_UnreadableHubRefuses(t *testing.T) {
	for _, row := range []struct {
		name, want string
		seed       func(*fakeGraphCaller)
	}{
		{"a failed read", "could not be read", func(fc *fakeGraphCaller) {
			fc.bulkQueryErr = errors.New("hub read failed")
		}},
		{"a TRUNCATED read", "TRUNCATED", func(fc *fakeGraphCaller) { fc.bulkTruncated = true }},
	} {
		t.Run(row.name, func(t *testing.T) {
			fc := practiceHubTargetFake(t)
			row.seed(fc)
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate", Arguments: json.RawMessage(hubResolvePayload("create", hubEndpointsHubA)),
			})
			require.True(t, handled)
			require.True(t, res.IsError)
			assert.Contains(t, toolResultText(res), row.want)
			assert.Empty(t, fc.execMutations)
		})
	}

	t.Run("the control: the same create serves on a whole read", func(t *testing.T) {
		_, _, res := driveHubTarget(t, hubResolvePayload("create", hubEndpointsHubA))
		assert.False(t, res.IsError, toolResultText(res))
	})
}

// TestPracticeHubArms_EveryRouteReachesTheEngineRules is the census's other axis:
// not which OPERATION, but which CALLER.
//
// WHY A SECOND AXIS. The operation census above drives the mutate tool, and every
// rule it observes could still be bypassed by a caller that does not go through
// that tool. Two do: the tools' own executeMutate funnel, which sixteen call
// sites share and which the recipe landing is one of, and the standalone `delete`
// tool.
// A route with no row is the hole the audit found twice, so the routes are listed
// here and each is driven with a hub that is not a hub.
func TestPracticeHubArms_EveryRouteReachesTheEngineRules(t *testing.T) {
	t.Run("executeMutate, the compile-and-execute funnel", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		_, err := executeMutate(opCtx(), fc, json.RawMessage(
			`{"operation":"create","graph":"practice","source_hub":"`+hubGhost+
				`","type":"pattern","name":"n","summary":"s"}`))
		require.Error(t, err, "the funnel runs the engine-layer hub rules")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("the standalone delete tool", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		res, err := engine.Dispatch(opCtx(), fc.Execute, fc.Stats, "delete", json.RawMessage(
			`{"graph":"practice","source":"`+hubGhost+`"}`))
		require.NoError(t, err)
		assert.True(t, res.IsError, "the standalone tool runs them too")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("the mutate tool, through the dispatch the intercept declines to", func(t *testing.T) {
		fc := practiceHubTargetFake(t)
		res, err := engine.Dispatch(opCtx(), fc.Execute, fc.Stats, "mutate", json.RawMessage(
			`{"operation":"create","graph":"practice","source_hub":"`+hubGhost+
				`","type":"pattern","name":"n","summary":"s"}`))
		require.NoError(t, err)
		assert.True(t, res.IsError)
		assert.Empty(t, fc.execMutations)
	})

	// THE ROUTE LIST IS DERIVED FROM THE TREE, and this is the half that failed
	// its own audit: it was a hand-written Go literal asserted only NotEmpty, which
	// cannot fail for any change to the code it claims to census.
	//
	// WHAT IS ASSERTED NOW. Every engine.Compile("mutate") site in the MODULE is
	// read out of the tree — from the module root, not this one package, because a
	// route that bypasses the funnel is likelier to be written in a sibling package
	// than beside the funnel it bypasses — and the per-file site counts must EQUAL
	// the census, which carries the guarded funnel plus every sibling site with the
	// reason it does not reach the practice hub rules. A new site fails BY NAME:
	// a new file appears as a key the census lacks, and a second site in a censused
	// file changes that file's count.
	assert.Equal(t, practiceCompileSiteCensus(), practiceMutateCompileSitesFromTree(t),
		"every engine.Compile(\"mutate\") site in this module is either the guarded funnel or a censused "+
			"sibling with a stated reason; a site in the derived set and not the census is a route nobody "+
			"has argued about")
}
