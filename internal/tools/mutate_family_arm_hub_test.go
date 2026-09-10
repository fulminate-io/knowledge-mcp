// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_family_arm_hub_test.go is the SECOND DIMENSION of the source_hub
// census, and it exists because the first one had a hidden axis.
//
// WHAT THE SIBLING ASSERTS. mutate_arm_polymorphic_hub_test.go drives every
// declared mutate operation on graph:"practice" and compares each one's observed
// disposition against a declared one. That closed the OPERATION axis: no
// operation can consume `source_hub` and do nothing with it.
//
// THE AXIS IT COULD NOT SEE. Every one of those rows fixes the graph at
// practice, so the table says nothing about what the same operation does on
// graph:"code", graph:"checks" or a registered custom family — and on those
// families four of the operations consumed the param and reached nothing, while
// a checks create stamped a practice hub onto a checks node through the shared
// create lowering. A one-dimensional table cannot express a defect that lives in
// the other dimension; it reports one green row per operation and is silent about
// the nine other cells behind it.
//
// SO THIS TABLE IS FAMILY x OPERATION. Both axes come from LIVE SOURCES rather
// than from an author's memory: the families from kgtypes.BuiltinGraphTypeNames
// (the same registry vocabulary the client's own selector refusals list) plus the
// graph-omitted default and a registered-custom stand-in, and the operations from
// mutateDeclaredOperations (the schema's own enum). A family or an operation
// added to either source lands in this grid with no cell declared for it and
// fails, rather than being discovered one review round at a time.
//
// THE DECLARED DISPOSITIONS. On practice, each operation's cell is exactly the
// one its sibling file already decided — this file does not restate them, it
// reads hubPolymorphicRows() — so the two tables cannot disagree. On EVERY other
// family, every operation's cell is REFUSED: `source_hub` names a practice source
// hub, no other family has one, and a param that can only reach nothing is bad
// input, which errors rather than being dropped.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// hubCustomFamilyProbe stands in for a graph type the custom registry admits.
// The refusal keys on "not practice" rather than on a known-family list, so a
// name no builtin claims is the shape that proves it — a gate written as an
// allow-list of foreign builtins would pass every cell above and fail this one.
const hubCustomFamilyProbe = "probe-custom-family"

// hubFamilyCase is one column of the grid: the family to drive, the label a
// failure names it by, and the instance selector that family needs for the
// payload to be well-formed.
//
// THE SELECTOR IS NOT DECORATION. requireGraphInstanceSelector refuses an
// instance-addressed family whose selector is empty, so a repo-less code payload
// would be claimed by THAT refusal and this row would report a refusal it did not
// cause. Carrying the selector is what makes the observed refusal attributable to
// the hub.
type hubFamilyCase struct {
	label    string
	graph    string
	selector string
}

// hubFamilyInstanceSelectors names the payload fragment each instance-addressed
// family needs. Families absent from the map are singletons and need none.
//
// IT IS CHECKED AGAINST THE REGISTRY rather than trusted:
// TestMutateFamilyArmHub_FamiliesComeFromTheRegistry fails when a key here names
// no live family, so a family renamed or retired cannot leave a stale entry that
// silently stops selecting anything.
func hubFamilyInstanceSelectors() map[string]string {
	return map[string]string{
		"code": `"repo":"probe-repo"`,
		"web":  `"name":"probe-instance"`,
		"pdf":  `"name":"probe-instance"`,
	}
}

// hubFamilyCases builds the family axis from the REGISTRY vocabulary, plus the
// two shapes the registry cannot name.
//
// THE GRAPH-OMITTED CASE IS A FAMILY, not a duplicate of knowledge. A caller that
// sends no `graph` is addressing the knowledge family by default, and it is the
// commonest non-practice mutate there is — a refusal that only fired on an
// explicit graph value would leave the whole default surface open while every
// explicit cell went green.
func hubFamilyCases(t *testing.T) []hubFamilyCase {
	t.Helper()
	names := kgtypes.BuiltinGraphTypeNames()
	require.NotEmpty(t, names, "the graph-type registry names no families — the family axis would be vacuous")
	selectors := hubFamilyInstanceSelectors()

	cases := []hubFamilyCase{{label: "<graph omitted>", graph: "", selector: ""}}
	for _, name := range names {
		cases = append(cases, hubFamilyCase{label: name, graph: name, selector: selectors[name]})
	}
	return append(cases, hubFamilyCase{
		label: "custom/" + hubCustomFamilyProbe, graph: hubCustomFamilyProbe, selector: `"name":"probe-instance"`,
	})
}

// hubFamilyPayload renders one cell's call. It reuses the sibling table's target
// fragments verbatim, so a cell drives the same well-formed shape its practice
// counterpart does and differs from it in the GRAPH alone.
func hubFamilyPayload(fam hubFamilyCase, operation, targets string) string {
	var b strings.Builder
	b.WriteString(`{"operation":"` + operation + `"`)
	if fam.graph != "" {
		b.WriteString(`,"graph":"` + fam.graph + `"`)
	}
	b.WriteString(`,"source_hub":"` + hubEndpointsHubA + `"`)
	for _, fragment := range []string{fam.selector, targets} {
		if fragment != "" {
			b.WriteString("," + fragment)
		}
	}
	b.WriteString("}")
	return b.String()
}

// hubFamilyDeclared is the declared cell for (family, operation).
//
// The practice column is READ FROM the sibling table rather than restated, which
// is what makes the two tables one specification: a disposition revised there
// moves here in the same edit, and a practice cell cannot be quietly relaxed in
// one file while the other still asserts it.
func hubFamilyDeclared(fam hubFamilyCase, operation string) hubDisposition {
	if fam.graph == string(kgtypes.GraphPractice) {
		return hubPolymorphicRows()[operation].declared
	}
	return hubDisposition{refused: true}
}

// TestMutateFamilyArmHub_EveryFamilyTimesEveryArm is the grid itself.
func TestMutateFamilyArmHub_EveryFamilyTimesEveryArm(t *testing.T) {
	rows := hubPolymorphicRows()
	require.NotEmpty(t, mutateDeclaredOperations, "the mutate schema declares no operations — the grid is vacuous")

	for _, fam := range hubFamilyCases(t) {
		for _, operation := range mutateDeclaredOperations {
			row, declaredRow := rows[operation]
			require.Truef(t, declaredRow,
				"operation %q has no target fragment in the sibling table, so cell %s/%s cannot be driven",
				operation, fam.label, operation)

			t.Run(fam.label+"/"+operation, func(t *testing.T) {
				payload := hubFamilyPayload(fam, operation, row.targets)
				got, body := observeHubDisposition(t, payload)
				want := hubFamilyDeclared(fam, operation)
				t.Logf("cell %s/%s → %s", fam.label, operation, hubDispositionLabel(got))

				assert.Equalf(t, want, got,
					"cell %s/%s: source_hub's observed disposition is not the declared one (result: %s)",
					fam.label, operation, body)
				if fam.graph == string(kgtypes.GraphPractice) {
					return
				}
				// THE REFUSAL HAS TO BE THE HUB'S. Every family here has other ways
				// to earn an error, so a bare IsError would be satisfied by an
				// unrelated rejection; the message must name the param, the family
				// the caller sent, and the family the param belongs to.
				assert.Containsf(t, body, practiceHubParamOnWrites,
					"cell %s/%s refused without naming the param the caller must remove", fam.label, operation)
				assert.Containsf(t, body, hubFamilyNamed(fam.graph),
					"cell %s/%s refused without naming the family it was sent to", fam.label, operation)
				assert.Containsf(t, body, string(kgtypes.GraphPractice),
					"cell %s/%s refused without naming the family the param does belong to", fam.label, operation)
			})
		}
	}
}

// hubDispositionLabel renders one observed cell for the -v table dump, so a run
// of this test IS the two-dimensional census rather than only its verdict.
func hubDispositionLabel(d hubDisposition) string {
	var facts []string
	for _, f := range []struct {
		on   bool
		name string
	}{
		{d.refused, "refused"}, {d.guardRead, "guard-read"},
		{d.inPlan, "on-the-plan"}, {d.noWrite, "no-write"},
	} {
		if f.on {
			facts = append(facts, f.name)
		}
	}
	if len(facts) == 0 {
		return "CONSUMED AND DROPPED (no refusal, no read, not on the plan, and a write went out)"
	}
	return strings.Join(facts, "+")
}

// TestMutateFamilyArmHub_FamiliesComeFromTheRegistry is the NO-SKIP guard for the
// family axis. The grid above could pass vacuously by driving a hand-picked
// subset, so the axis is counted against the live registry and the selector map
// is held to it.
func TestMutateFamilyArmHub_FamiliesComeFromTheRegistry(t *testing.T) {
	names := kgtypes.BuiltinGraphTypeNames()
	require.NotEmpty(t, names, "the registry names no families")

	driven := map[string]bool{}
	for _, fam := range hubFamilyCases(t) {
		driven[fam.graph] = true
	}
	for _, name := range names {
		assert.Truef(t, driven[name],
			"family %q is in the registry vocabulary and has no cell — an undriven family is the "+
				"silent drop this grid exists to prevent", name)
	}
	assert.True(t, driven[""], "the graph-omitted default must be driven; it is the commonest call shape")
	assert.True(t, driven[hubCustomFamilyProbe],
		"a family outside the builtin vocabulary must be driven, or an allow-list gate would pass")

	// The selector map is held to the registry too: a key naming no live family
	// selects nothing and would leave its cells asserting against a shape no
	// caller can send.
	for family := range hubFamilyInstanceSelectors() {
		assert.Truef(t, slices.Contains(names, family),
			"the instance-selector map names %q, which is not a live family", family)
	}
}

// TestMutateFamilyArmHub_NoParamPathIsUntouched is requirement 3: the refusal
// must add nothing to the wire on the calls that do NOT name a hub.
//
// IT OBSERVES THE PRODUCTION GATE BY NAME. refusePracticeHubOffFamily is the one
// function every arm's refusal now runs through, so "returns nil for an empty
// hub, on every family and every operation" is the whole no-param contract — a
// gate that fired on an empty hub would be caught here on the first cell rather
// than by a caller whose unrelated write started failing.
//
// AND IT CARRIES ITS OWN KNOWN POSITIVE, in the same run, on the same instrument:
// the identical args with the hub POPULATED must refuse on every foreign family.
// Without that pair a gate deleted outright would satisfy the nil half of every
// row and this test would go green on no gate at all.
func TestMutateFamilyArmHub_NoParamPathIsUntouched(t *testing.T) {
	for _, fam := range hubFamilyCases(t) {
		for _, operation := range mutateDeclaredOperations {
			t.Run(fam.label+"/"+operation, func(t *testing.T) {
				empty := mutateArgs{Operation: operation, Graph: fam.graph}
				require.NoErrorf(t, refusePracticeHubOffFamily(empty),
					"cell %s/%s names no hub, so the gate must return before deciding anything",
					fam.label, operation)

				populated := mutateArgs{Operation: operation, Graph: fam.graph, SourceHub: hubEndpointsHubA}
				err := refusePracticeHubOffFamily(populated)
				if fam.graph == string(kgtypes.GraphPractice) {
					require.NoErrorf(t, err, "the practice family is the one family the hub belongs to")
					return
				}
				require.Errorf(t, err,
					"known positive: the same cell WITH a hub must refuse, or the nil above proves nothing")
			})
		}
	}
}

// hubFamilyNamed renders the family a refusal must name. The graph-omitted call
// addresses knowledge, so that is the family its message names — reporting the
// empty string back to a caller who typed nothing would name nothing.
func hubFamilyNamed(graph string) string {
	if graph == "" {
		return string(kgtypes.GraphKnowledge)
	}
	return graph
}

// TestMutateFamilyArmHub_ChecksCreateCarriesNoHub is the checks half stated on its
// own, because it is the one cell whose PRE-CHANGE behaviour was a write rather
// than a drop and so the one a reader will want to see named.
//
// The shared create lowering (engine.withSourceHub and engine.sourceHubEdges) is
// family-blind: it stamps whenever the arg is set, and handleGraphPassthroughMutate
// admits practice AND checks, so a checks create used to land a practice hub key
// and a sourced-from edge on a checks node. Refusing the param above that arm is
// what removes it; this asserts the outcome rather than the mechanism, so the
// mechanism stays free to change.
func TestMutateFamilyArmHub_ChecksCreateCarriesNoHub(t *testing.T) {
	for _, operation := range []string{"create", "create_batch"} {
		t.Run(operation, func(t *testing.T) {
			row := hubPolymorphicRows()[operation]
			fam := hubFamilyCase{label: checksGraphSelector, graph: checksGraphSelector}
			fc := practiceHubTargetFake(t)
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name:      "mutate",
				Arguments: json.RawMessage(hubFamilyPayload(fam, operation, row.targets)),
			})
			require.True(t, handled, "a checks %s carrying source_hub must be CLAIMED, not passed on", operation)
			require.Truef(t, res.IsError, "and refused: %s", toolResultText(res))
			assert.Empty(t, fc.execMutations,
				"nothing may be written: the refusal precedes the create lowering that used to stamp the hub")

			// THE SAME CALL WITHOUT THE PARAM still writes, which is what makes the
			// refusal above a refusal of the PARAM rather than of checks creates.
			control := practiceHubTargetFake(t)
			payload := strings.Replace(hubFamilyPayload(fam, operation, row.targets),
				`"source_hub":"`+hubEndpointsHubA+`",`, "", 1)
			require.NotContains(t, payload, practiceHubParamOnWrites,
				"the control payload must genuinely carry no hub")
			_, controlRes := InterceptMutate(opCtx(), interceptTestDeps{gc: control}, kgtools.CallToolParams{
				Name: "mutate", Arguments: json.RawMessage(payload),
			})
			assert.Falsef(t, controlRes.IsError,
				"control: a checks %s naming no hub is untouched by this change: %s",
				operation, toolResultText(controlRes))
			assert.NotContains(t, planText(t, control.execMutations[len(control.execMutations)-1]),
				kgtypes.MetaKeySourceHub,
				"control: a checks %s writes no %s key of its own", operation, kgtypes.MetaKeySourceHub)
		})
	}
}

// hubDocFamilyList renders the family enumeration a source_hub doc must carry:
// every registry family EXCEPT practice, quoted and comma-separated, in registry
// order. Deriving it here rather than typing it is what makes the two doc
// assertions below a real check — a family added to the registry and left out of
// the documentation fails, instead of leaving a caller to discover the refusal.
func hubDocFamilyList() string {
	var quoted []string
	for _, name := range kgtypes.BuiltinGraphTypeNames() {
		if name == string(kgtypes.GraphPractice) {
			continue
		}
		quoted = append(quoted, `"`+name+`"`)
	}
	return strings.Join(quoted, ", ")
}

// hubDocOperationList renders the operation enumeration, from the schema's own
// enum in its own order, for the same reason.
func hubDocOperationList() string {
	return strings.Join(mutateDeclaredOperations, ", ")
}

// hubDocNormalize collapses runs of whitespace, so a doc that WRAPS its sentence
// across lines still matches one literal. Without it every assertion below would
// be pinned to a line break and would fail on a reflow that changed no words.
func hubDocNormalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestMutateFamilyArmHub_DocumentationSaysPracticeOnly is requirement 15: the two
// surfaces a caller actually reads must say the rule the code now enforces.
//
// IT READS THE RENDERED HELP, not the const behind it: help("mutate") is what an
// LLM caller receives, and a const that never reached the render would satisfy a
// const-level assertion while telling nobody anything.
func TestMutateFamilyArmHub_DocumentationSaysPracticeOnly(t *testing.T) {
	families, operations := hubDocFamilyList(), hubDocOperationList()
	require.NotEmpty(t, families, "the registry named no families, so the doc assertion would be vacuous")
	require.NotEmpty(t, operations, "the schema declared no operations, so the doc assertion would be vacuous")

	handled, res := InterceptHelp(opCtx(), interceptTestDeps{}, kgtools.CallToolParams{
		Name: "help", Arguments: json.RawMessage(`{"topic":"mutate"}`),
	})
	require.True(t, handled, `help("mutate") must be served by the client`)
	require.False(t, res.IsError, "help(\"mutate\") errored: %s", toolResultText(res))
	rendered := hubDocNormalize(toolResultText(res))

	prop, declared := mutateProperties()[practiceHubParamOnWrites]
	require.Truef(t, declared, "the schema must declare %q", practiceHubParamOnWrites)
	schema := hubDocNormalize(prop.Description)

	for _, surface := range []struct{ name, body string }{
		{`help("mutate")`, rendered}, {"the mutate schema's source_hub description", schema},
	} {
		t.Run(surface.name, func(t *testing.T) {
			assert.Contains(t, surface.body, "PRACTICE-FAMILY PARAMETER ON EVERY ARM",
				"the practice-only rule must be stated, not left to be discovered by a refusal")
			assert.Contains(t, surface.body, "REFUSED naming the family on every family but practice",
				"and stated as a refusal rather than as a preference")
			assert.Contains(t, surface.body, "never consumed and dropped",
				"the reader must be told the param is not silently ignored")
			assert.Containsf(t, surface.body, families,
				"the family enumeration must be the registry's own, in its order: %s", families)
			assert.Containsf(t, surface.body, operations,
				"the operation enumeration must be the schema's own, in its order: %s", operations)
			assert.Contains(t, surface.body, "An omitted",
				"and the omitted-graph default must be named, since it is the commonest call shape")
		})
	}

	// SAME-RUN KNOWN POSITIVE. Holing the family enumeration out of the rendered
	// help must make the same assertion fail, so a green run above is evidence the
	// literal is present rather than evidence the matcher never looks.
	holed := strings.ReplaceAll(rendered, families, "")
	assert.NotContains(t, holed, families,
		"known positive: with the enumeration removed the assertion must no longer hold")
}

// The CONFLICTING-BODY cell of the grid. Its rows live here rather than beside
// the body-conflict class they belong to because the FAMILY axis is this file's,
// and a cell driven off a hand-picked family list is the vacuous pass
// TestMutateFamilyArmHub_FamiliesComeFromTheRegistry exists to prevent.
// TestMutateFamilyArmHub_ConflictingBodyHubOnEveryFamily is the family axis for this
// class, driven on the same registry vocabulary the grid uses. On practice the
// body gate refuses; on every other family the off-family gate does. The cell
// that matters is the practice one — the others prove the new gate did not
// create a family-shaped hole beside itself.
func TestMutateFamilyArmHub_ConflictingBodyHubOnEveryFamily(t *testing.T) {
	for _, fam := range hubFamilyCases(t) {
		for _, operation := range []string{"create", "create_batch", "upsert"} {
			t.Run(fam.label+"/"+operation, func(t *testing.T) {
				payload := hubFamilyPayload(fam, operation, hubBodyConflictTargets(operation))
				got, body := observeHubDisposition(t, payload)
				assert.Truef(t, got.refused,
					"cell %s/%s: a body naming another hub must be refused, not written (result: %s)",
					fam.label, operation, body)
			})
		}
	}
}

// hubBodyConflictTargets renders the family grid's target fragment for one arm
// with a CONFLICTING body hub spliced in. It mirrors the sibling table's
// fragments so a cell differs from its plain counterpart in the body hub alone.
func hubBodyConflictTargets(operation string) string {
	conflict := hubBodyMetaFragment(hubEndpointsHubB)
	switch operation {
	case "create":
		return `"type":"pattern","name":"probe-name","summary":"probe summary",` + conflict
	case "create_batch":
		return `"nodes":[{"type":"pattern","name":"probe-name","summary":"probe summary",` + conflict + `}]`
	case "upsert":
		return `"id":"` + hubTargetMemberA + `","type":"pattern","name":"n","summary":"s",` + conflict
	}
	return ""
}
