// SPDX-License-Identifier: Apache-2.0

package tools

// query_param_accounting_test.go asserts that queryArmRegistry PARTITIONS the
// live query schema on every arm — the analog of mutate_schema_parity_test.go
// for the query surface.
//
// WHY A PARTITION AND NOT A SUBSET. A subset check ("no arm names a key the
// schema does not declare") is satisfiable by a registry that classifies
// nothing. The partition is what makes the registry a STATEMENT: every one of
// the 61 declared params lands in exactly one of each arm's three sets, so a
// param added to query_schema.go lands in NO set and fails here until someone
// classifies it. That is the no-runtime-complement rule enforced rather than
// merely documented.
//
// THE CLOSED ALLOWLIST IS ASSERTED, not trusted to prose. Without subtest
// "ignored keys stay inside the closed allowlist", "this entry point serves
// multiple shapes" is a non-empty justification and every routing param an arm
// drops can be parked in deliberatelyIgnored while the parity harness's
// behaves-and-probe-absent assertion still holds — the exact route by which a
// fully green registry still silently drops arm-inapplicable params.

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// queryArgsPayload re-marshals a decoded queryArgs back into the wire payload
// the accounting gate reads. Tests that drive a router DIRECTLY (rather than
// through its intercept) have no params.Arguments to thread, and a nil payload
// makes the gate fail closed by design. Deriving the payload from the same args
// the router receives reproduces the production relationship exactly — in
// production params.Arguments is what queryArgs was decoded FROM — so a test
// payload can never claim a param the test did not set.
func queryArgsPayload(a queryArgs) json.RawMessage {
	raw, err := json.Marshal(a)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// gatedRoutePractice / gatedRouteLinkage / gatedRouteWebPDF drive the three
// payload-threaded routers with a payload derived from their own args. They
// exist so the router tests keep asserting ROUTING rather than restating the
// gate, which has its own suite.
func gatedRoutePractice(ctx context.Context, deps ClientDeps, gc statsRPC, a queryArgs) kgtools.ToolResult {
	return routePracticeClient(ctx, deps, gc, a, queryArgsPayload(a))
}

func gatedRouteLinkage(ctx context.Context, gc statsRPC, a queryArgs) kgtools.ToolResult {
	return routeLinkageClient(ctx, gc, a, queryArgsPayload(a))
}

func gatedRouteWebPDF(a queryArgs) (bool, kgtools.ToolResult) {
	return routeWebPDFClient(a, queryArgsPayload(a))
}

// queryRenderAllowlist is the CLOSED set of params an arm may deliberately
// ignore. Both are render params: an arm that renders its own result and never
// consults them ignores them with a justification rather than rejecting a
// schema-advertised render shape. Nothing else may be parked here.
var queryRenderAllowlist = map[string]bool{"format": true, "fields": true}

// queryParamGroupTable is every frozen group in query_arm_registry.go, so the
// group-level partition below cannot silently miss one that was added to the
// file but wired into no arm.
var queryParamGroupTable = map[string][]string{
	"qgSelector": qgSelector,
	"qgIdentity": qgIdentity,
	"qgText":     qgText,
	"qgPaging":   qgPaging,
	"qgRender":   qgRender,
	"qgCode":     qgCode,
	"qgThought":  qgThought,
	"qgSimulate": qgSimulate,
	"qgTopology": qgTopology,
	"qgPivot":    qgPivot,
	"qgStats":    qgStats,
	"qgCloud":    qgCloud,
	"qgRules":    qgRules,
}

// TestQueryParamGroups_PartitionSchema proves the thirteen frozen groups name
// each declared param EXACTLY ONCE. This is the group-level twin of the per-arm
// partition: because the arms compose their sets from these groups, a schema
// addition that lands in no group is caught here with a single clear failure
// rather than as 47 identical per-arm failures.
func TestQueryParamGroups_PartitionSchema(t *testing.T) {
	schema := QueryToolDef().InputSchema.Properties
	require.NotEmpty(t, schema, "QueryToolDef must declare params")

	seenIn := map[string]string{} // param → the group that named it
	for _, groupName := range sortedGroupNames() {
		for _, key := range queryParamGroupTable[groupName] {
			if prior, dup := seenIn[key]; dup {
				assert.Failf(t, "param named by two groups",
					"%q appears in both %s and %s — the groups must partition, not overlap",
					key, prior, groupName)
				continue
			}
			seenIn[key] = groupName
			assert.Containsf(t, schema, key,
				"group %s names %q which QueryToolDef does not declare — a stale group entry",
				groupName, key)
		}
	}

	var unnamed []string
	for param := range schema {
		if _, ok := seenIn[param]; !ok {
			unnamed = append(unnamed, param)
		}
	}
	sort.Strings(unnamed)
	assert.Emptyf(t, unnamed,
		"these declared params are in no frozen group, so no arm can name them and every arm's "+
			"partition will fail: %v — add each to the group it belongs to and give it a cell", unnamed)

	assert.Len(t, seenIn, queryDeclaredParamCount,
		"the groups must name exactly the declared param count between them")
}

func sortedGroupNames() []string {
	names := make([]string, 0, len(queryParamGroupTable))
	for name := range queryParamGroupTable {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestQueryArmRegistry_IsOneObjectAssembledFromItsSiblings proves the three
// sibling table files feed ONE registry rather than standing as three
// independent ones. It checks the sum arithmetic (so a group that failed to be
// copied cannot hide behind the total) and then reads one arm from each sibling
// back out of the assembled map — the gate and the tests must see the same
// object or the classification they enforce and the classification they assert
// can drift apart.
func TestQueryArmRegistry_IsOneObjectAssembledFromItsSiblings(t *testing.T) {
	require.Len(t, queryArmRegistry,
		len(queryGraphArmSpecs)+len(queryModeArmSpecs)+len(queryReflectArmSpecs),
		"every sibling group must be copied into the one registry, and no armID may collide "+
			"across groups (a collision silently overwrites and shrinks the total)")

	for _, arm := range []armID{armCloudCICDStats, armCorrelations, armReflectRecall} {
		spec, ok := queryArmRegistry[arm]
		assert.Truef(t, ok, "arm %s is declared in a sibling file but absent from the assembled registry", arm)
		assert.NotEmptyf(t, spec.handler, "arm %s came through the assembly without its handler", arm)
	}
}

// TestQueryArmRegistry_DeclaresEveryArm pins the arm total. A literal count, so
// a table edit that drops an arm (or duplicates a key and silently overwrites
// one) fails rather than moving the target with it.
func TestQueryArmRegistry_DeclaresEveryArm(t *testing.T) {
	assert.Len(t, queryArmRegistry, queryArmCount,
		"the query arm count is locked by plan; a new claim point needs an armID, a cell row, "+
			"and a gate call site")

	for arm, spec := range queryArmRegistry {
		assert.NotEmptyf(t, spec.operation, "arm %s declares no operation", arm)
		assert.NotEmptyf(t, spec.handler, "arm %s declares no handler — the tie back to source", arm)
		assert.NotEmptyf(t, spec.rejectedSorted,
			"arm %s has an empty precomputed rejected order; init did not run over it", arm)
		assert.Lenf(t, spec.rejectedSorted, len(spec.rejected),
			"arm %s: the precomputed order must cover its whole rejected set", arm)
	}
}

// TestQueryArmAccounting_TablePartitionsSchemaPerArm is the contract: per arm,
// the three sets partition the live 61-key schema exactly, and every ignored
// cell is justified AND inside the closed render allowlist.
func TestQueryArmAccounting_TablePartitionsSchemaPerArm(t *testing.T) {
	schema := QueryToolDef().InputSchema.Properties
	require.NotEmpty(t, schema, "QueryToolDef must declare params")
	require.NotEmpty(t, queryArmRegistry, "the registry must be assembled — an empty table proves nothing")

	for _, arm := range sortedArmIDs() {
		spec := queryArmRegistry[arm]
		t.Run(string(arm), func(t *testing.T) {
			t.Run("covers every declared param exactly once", func(t *testing.T) {
				missing, doubled := partitionDiff(spec, schema)
				assert.Emptyf(t, missing,
					"arm %s classifies none of %v — every schema param needs a cell, and a NEW schema "+
						"param must be classified rather than defaulting to anything", arm, missing)
				assert.Emptyf(t, doubled,
					"arm %s classifies %v in more than one set — the three sets must be disjoint", arm, doubled)
			})

			t.Run("names no key the schema does not declare", func(t *testing.T) {
				for _, key := range sortedKeys(spec.consumed) {
					assert.Containsf(t, schema, key, "arm %s consumes undeclared param %q", arm, key)
				}
				for _, key := range spec.rejectedSorted {
					assert.Containsf(t, schema, key, "arm %s rejects undeclared param %q", arm, key)
				}
				for key := range spec.deliberatelyIgnored {
					assert.Containsf(t, schema, key, "arm %s ignores undeclared param %q", arm, key)
				}
			})

			t.Run("ignored keys stay inside the closed allowlist", func(t *testing.T) {
				for key, justification := range spec.deliberatelyIgnored {
					assert.Truef(t, queryRenderAllowlist[key],
						"arm %s deliberately ignores %q, which is not one of the two render params — a param "+
							"that is neither consumed nor a render param is REJECTED; there is no third option",
						arm, key)
					assert.NotEmptyf(t, justification,
						"arm %s ignores %q with no justification", arm, key)
				}
			})
		})
	}

	// KNOWN POSITIVE for the emptiness assertions above. With every arm
	// partitioning cleanly, "missing and doubled are empty" is also satisfied by
	// a comparison that measures nothing, so drive the SAME function with a spec
	// that is deliberately short one key and one that double-classifies, and
	// confirm each is reported.
	t.Run("the partition check reports a real gap", func(t *testing.T) {
		probe := queryArmRegistry[armKnowledgeStats]
		require.NotEmpty(t, probe.consumed, "the probe arm must have a consumed set to break")

		short := armSpec{
			consumed:            copyBoolSet(probe.consumed),
			rejected:            copyBoolSet(probe.rejected),
			deliberatelyIgnored: probe.deliberatelyIgnored,
		}
		delete(short.consumed, "format")
		missing, doubled := partitionDiff(short, QueryToolDef().InputSchema.Properties)
		assert.Equal(t, []string{"format"}, missing,
			"removing a cell must be reported as an unclassified param — otherwise the per-arm "+
				"emptiness assertions above are vacuous")
		assert.Empty(t, doubled)

		overlap := armSpec{
			consumed:            copyBoolSet(probe.consumed),
			rejected:            copyBoolSet(probe.rejected),
			deliberatelyIgnored: probe.deliberatelyIgnored,
		}
		overlap.rejected["format"] = true
		missing, doubled = partitionDiff(overlap, QueryToolDef().InputSchema.Properties)
		assert.Empty(t, missing)
		assert.Equal(t, []string{"format"}, doubled,
			"classifying one param twice must be reported — otherwise the disjointness assertion is vacuous")
	})
}

// partitionDiff reports the declared params an arm classifies in NO set and the
// ones it classifies in MORE THAN ONE. Both slices are sorted so a failure names
// the same key first every run.
func partitionDiff[T any](spec armSpec, schema map[string]T) (missing, doubled []string) {
	for param := range schema {
		hits := 0
		if spec.consumed[param] {
			hits++
		}
		if spec.rejected[param] {
			hits++
		}
		if _, ignored := spec.deliberatelyIgnored[param]; ignored {
			hits++
		}
		switch {
		case hits == 0:
			missing = append(missing, param)
		case hits > 1:
			doubled = append(doubled, param)
		}
	}
	sort.Strings(missing)
	sort.Strings(doubled)
	return missing, doubled
}

func sortedArmIDs() []armID {
	arms := make([]armID, 0, len(queryArmRegistry))
	for arm := range queryArmRegistry {
		arms = append(arms, arm)
	}
	slices.Sort(arms)
	return arms
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func copyBoolSet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	maps.Copy(out, in)
	return out
}

// TestQueryArmRegistry_ClassificationRoutesThroughTheSharedPrimitive proves the
// query registry is read by the SAME registryParamClass switch mutate uses,
// rather than by a second copy of the classification rule. A second copy would
// be two statements of one contract, drifting the moment either is edited.
func TestQueryArmRegistry_ClassificationRoutesThroughTheSharedPrimitive(t *testing.T) {
	class, ok := queryParamClass(armKnowledgeStats, "samples")
	require.True(t, ok, "samples must be classified on the knowledge stats arm")
	assert.Equal(t, classConsumed, class, "knowledgeStats reads a.Samples")

	class, ok = queryParamClass(armKnowledgeStats, "limit")
	require.True(t, ok, "limit must be classified on the knowledge stats arm")
	assert.Equal(t, classRejected, class,
		"a Stats RPC request has nowhere to put a limit — the drop the Phase-1 reproduction measured")

	class, ok = queryParamClass(armKnowledgeStats, "fields")
	require.True(t, ok, "fields must be classified on the knowledge stats arm")
	assert.Equal(t, classDeliberatelyIgnored, class,
		"fields is a render param this arm does not project with")

	_, ok = queryParamClass("armNoSuchQueryArm", "format")
	assert.False(t, ok, "an unregistered arm must report unclassified, never a silent default")
}
