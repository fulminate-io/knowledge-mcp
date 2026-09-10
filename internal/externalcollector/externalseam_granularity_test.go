// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// externalseam_granularity_test.go — WHAT A SKIP ENDS, and what survives it.
// Split from externalseam_run_test.go, which keeps the seam's whole-run
// observations and the helpers all three files share; this file is the skip's
// granularity and the fork-bomb guard that rides the same child spawn. No
// assertion moved with the split.

// TestExternalCollectorSeam_SkipGranularityFollowsTheSubtestStructure pins the
// half a naive implementation gets wrong: where a registration is built decides
// what a skip ends.
//
// IT IS ALSO THE ROW THAT CORRECTS A LEXICAL READING OF THESE TESTS. Three of
// the eleven — CompletenessAssertion, DanglingEdgeIsAdmitted and
// EmptyGraphIsAdmitted — write their stdioDef call in the TOP-LEVEL body inside
// a table, but the call is made through a closure the SUBTEST invokes, so the
// skip ends the stdio subtest and the in-process http sibling keeps running.
// Reading the call site's line number says otherwise; this asserts the runtime
// behaviour.
func TestExternalCollectorSeam_SkipGranularityFollowsTheSubtestStructure(t *testing.T) {
	if os.Getenv(seamNestedEnv) != "" {
		t.Skip("this test spawns a child run of the eleven; it does not run inside one")
	}
	command, _ := writeExternalConformanceStub(t)

	subtestScoped, code := runChild(t, dialingRunFilter(), map[string]string{
		seamNestedEnv:        "1",
		externalCollectorEnv: command,
		unemulatedModesEnv: strings.Join([]string{
			stubModeBadInputSchema, stubModeDanglingEdge, stubModeEmptyGraph, stubModeIncompleteWalk,
		}, ","),
	})
	require.Equal(t, 0, code, "%s", subtestScoped)
	lines := resultLines(subtestScoped)

	assert.Contains(t, lines, "    --- SKIP: TestRunMCP_SchemaGateRefusals/stdio/input_schema_not_requiring_the_collect_id",
		"a mode only a subtest drives ends THAT subtest")
	assert.Contains(t, lines, "    --- PASS: TestRunMCP_SchemaGateRefusals/http/input_schema_not_requiring_the_collect_id",
		"and its siblings keep running — the surviving-sibling half is what makes the granularity worth anything")
	assert.Contains(t, lines, "--- PASS: TestRunMCP_SchemaGateRefusals",
		"a test whose subtest skipped is not itself skipped")
	assert.Contains(t, lines, "    --- SKIP: TestVerifyRegistration_RefusesAtRegisterToo/bad-input-schema",
		"the same holds for the refusal loop's own rows")
	assert.Contains(t, lines, "    --- SKIP: TestRunMCP_DanglingEdgeIsAdmitted/stdio",
		"the stdio arm of a table-driven test is a subtest, whatever line the registration is written on")
	assert.Contains(t, lines, "    --- PASS: TestRunMCP_DanglingEdgeIsAdmitted/http",
		"so the in-process http assertion the seam has no business reaching keeps running")
	// ALL FIVE OF THE CASES THE PREFILL ENUMERATES ARE NAMED HERE, which is what
	// stops the count shrinking quietly: the two remaining ones are subtest-scoped
	// at runtime like DanglingEdge, and each is asserted on BOTH halves — its own
	// stdio SKIP and its http sibling's survival — rather than through the sibling
	// alone.
	assert.Contains(t, lines, "    --- SKIP: TestRunMCP_EmptyGraphIsAdmitted/stdio",
		"the fifth case is subtest-scoped too, and naming it is what keeps the five from becoming four")
	assert.Contains(t, lines, "    --- PASS: TestRunMCP_EmptyGraphIsAdmitted/http")
	assert.Contains(t, lines, "    --- SKIP: TestRunMCP_CompletenessAssertion/stdio",
		"its own skip, not merely the survival of its sibling")
	assert.Contains(t, lines, "    --- PASS: TestRunMCP_CompletenessAssertion/http")

	wholeTest, code := runChild(t, dialingRunFilter(), map[string]string{
		seamNestedEnv:        "1",
		externalCollectorEnv: command,
		unemulatedModesEnv:   stubModeConforming,
	})
	require.Equal(t, 0, code, "%s", wholeTest)
	whole := resultLines(wholeTest)
	assert.Contains(t, whole, "--- SKIP: TestRunMCP_ConformingProvider_BothTransports",
		"a registration built in a top-level body ends the WHOLE test, including its in-process http arm")
	assert.Contains(t, whole, "--- SKIP: TestVerifyRegistration_RefusesAtRegisterToo",
		"and here it ends the test above its own subtest loop, taking the four refusal subtests with it")
	assert.Contains(t, whole, "    --- PASS: TestRunMCP_CompletenessAssertion/http",
		"while a table's http arm, built in its own subtest, is untouched")
}

// TestExternalCollectorSeam_TheForkBombGuardSurvivesTheSeam pins the guard the
// stub harness records as having taken this machine to a load average in the
// hundreds: a marked child with no mode exits rather than running the suite. The
// seam changes the command a child is spawned with, so the guard is re-asserted
// with the seam on.
func TestExternalCollectorSeam_TheForkBombGuardSurvivesTheSeam(t *testing.T) {
	if os.Getenv(seamNestedEnv) != "" {
		t.Skip("this test spawns a child run; it does not run inside one")
	}
	command, _ := writeExternalConformanceStub(t)
	const guard = "TestStubHarness_AMarkedChildWithNoModeExitsInsteadOfRunningTheSuite"
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"seam off", map[string]string{seamNestedEnv: "1"}},
		{"seam on", map[string]string{seamNestedEnv: "1", externalCollectorEnv: command}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runChild(t, "^"+guard+"$", tc.env)
			assert.Equal(t, 0, code, "%s", out)
			assert.Contains(t, resultLines(out), "--- PASS: "+guard)
		})
	}
}
