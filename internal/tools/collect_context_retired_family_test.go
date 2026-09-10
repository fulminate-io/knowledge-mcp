// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_context_retired_family_test.go — THE FILL PATH'S OWN RETIREMENT
// REFUSAL, which nothing else observes.
//
// WHY IT NEEDS ITS OWN TEST, and how that was established. The add-time refusal
// in CollectContext.Validate is covered by the externalcollector and
// collectorconfig suites, and it is the one an operator meets when they run
// `collector add` today. It is NOT the one that runs on the entry already on
// disk. A collectors.json written a release ago carries `cloud` or `logs` in its
// context declaration and never passes through add-time validation again — the
// daemon reads it and fills. validateContextFamilies is the only thing standing
// between that entry and a fill that reports the retired family as merely
// unregistered.
//
// MEASURED, NOT SUPPOSED: deleting the retirement arm from
// validateContextFamilies left the ENTIRE tools package green. That is the
// signature of an unobserved guard, and it is why this file exists.
//
// THE TWO ARMS ARE DIFFERENT SENTENCES ON PURPOSE. A retired family is named as
// RETIRED with what replaced it; a name that was never a family is named as
// UNREGISTERED with the families this daemon can supply. Telling an operator
// whose entry worked last release that they had made a typo is the failure the
// retirement map exists to prevent, so the control below asserts the second
// sentence is NOT the first.
func TestFillCollectContext_ARetiredFamilyIsRefusedAsRetired(t *testing.T) {
	for _, family := range []string{"cloud", "logs"} {
		t.Run(family, func(t *testing.T) {
			// The registry holds a real custom type, so a refusal here cannot be
			// "the registry was empty" wearing the retirement's clothes.
			deps := &customDeps{crud: registeredCRUD("acme-tracker")}

			_, _, err := fillCollectContext(context.Background(), deps,
				externalcollector.ContextDeclaration{family: {
					NodeTypes:  []string{"anything"},
					NodeFields: []string{externalcollector.ContextNodeFieldID},
				}})
			require.Errorf(t, err,
				"an on-disk entry still declaring the retired family %q must be REFUSED at fill "+
					"time — it never passes through add-time validation again", family)
			assert.Containsf(t, err.Error(), "retired",
				"and the refusal must say the family was REMOVED: %v", err)
			assert.Containsf(t, err.Error(), family, "naming the value it rejected: %v", err)
			// AND THE ROUTE THAT REPLACED IT, AS IT ACTUALLY EXISTS. This asserted
			// "custom_collector" and was satisfied by a sentence pointing at that
			// tool's register operation, which it does not have: its enum is
			// [list], and registration is a config file installed with the
			// collector-add command.
			assert.Containsf(t, err.Error(), "knowledge collector add",
				"the refusal must name the command that registers a collector: %v", err)
			assert.Containsf(t, err.Error(), "collectors.json",
				"and the file the registration IS: %v", err)
		})
	}
}

// TestFillCollectContext_AnUnregisteredFamilyIsRefusedAsUnregistered is the
// CONTROL, and it carries the whole discrimination. Without it, a fill path that
// refused every non-code family with the retirement sentence would satisfy every
// assertion above while lying about names that were never families at all.
func TestFillCollectContext_AnUnregisteredFamilyIsRefusedAsUnregistered(t *testing.T) {
	deps := &customDeps{crud: registeredCRUD("acme-tracker")}

	_, _, err := fillCollectContext(context.Background(), deps,
		externalcollector.ContextDeclaration{"zzz-never-a-family": {
			NodeTypes:  []string{"anything"},
			NodeFields: []string{externalcollector.ContextNodeFieldID},
		}})
	require.Error(t, err, "a family this daemon cannot supply is still refused")
	assert.NotContains(t, err.Error(), "retired",
		"a name that never was a family is UNREGISTERED, not retired — the two sentences send an "+
			"operator to different places, and conflating them is the defect the retirement map exists to prevent")

	// AND THE SECOND CONTROL, in the same run: a family the registry DOES hold
	// fills without error, so neither assertion above is satisfied by a path that
	// refuses everything. It needs a graph caller as well as a registry row — a
	// declared family with no caller is refused for that reason instead, which
	// would make this control pass for the wrong cause.
	supplied := &customDeps{
		crud: registeredCRUD("acme-tracker"),
		graphs: &contextGraphCaller{
			names: map[string][]string{"acme-tracker": {"acme-prod"}},
			graphs: map[string]map[string][]*knowledgev1.Node{"acme-tracker": {"acme-prod": {
				{Id: "t/1", Type: "anything", SymbolName: "one"},
			}}},
		},
	}
	block, _, err := fillCollectContext(context.Background(), supplied,
		externalcollector.ContextDeclaration{"acme-tracker": {
			NodeTypes:  []string{"anything"},
			NodeFields: []string{externalcollector.ContextNodeFieldID},
		}})
	require.NoError(t, err, "control: a registered family with a reachable graph is supplyable and fills")
	require.NotNil(t, block)
	assert.NotEmpty(t, (*block)["acme-tracker"],
		"control: and the fill actually returns that family's graphs, so the no-error above is not a "+
			"path that returned early having read nothing")
}

// TestFillCollectContext_ADeclaredFamilyWithNoGraphsIsAnEmptySliceNotAMissingKey
// pins the EMPTY-BLOCK CONTRACT that three doc blocks state in the same words —
// externalcollector's CollectContext ("its value is an empty slice rather than a
// missing key"), the framework's ForeignContext, and this package's own fill
// loop ("ASSIGNED EVEN WHEN EMPTY") — and that no test drove.
//
// WHY THE DISTINCTION IS LOAD-BEARING RATHER THAN PEDANTIC. A module reads the
// block to decide what it can resolve against. An empty slice says "you asked
// and the store held nothing", which is an answer it can act on — it emits no
// correlation and says so. A missing key says "you did not ask", which is the
// same shape a dropped declaration produces, and a module cannot tell a store
// with nothing in it from a client that never filled its request.
//
// THE FIXTURE IS THE REAL ONE: a family the registry HOLDS, whose graph list is
// empty. A family the registry does not hold is refused, which is a different
// arm and is covered above.
func TestFillCollectContext_ADeclaredFamilyWithNoGraphsIsAnEmptySliceNotAMissingKey(t *testing.T) {
	deps := &customDeps{
		crud:   registeredCRUD("acme-tracker"),
		graphs: &contextGraphCaller{names: map[string][]string{}},
	}

	block, _, err := fillCollectContext(context.Background(), deps,
		externalcollector.ContextDeclaration{"acme-tracker": {
			NodeTypes:  []string{"anything"},
			NodeFields: []string{externalcollector.ContextNodeFieldID},
		}})
	require.NoError(t, err,
		"a registered family holding no graphs is a legitimate answer, not an error")
	require.NotNil(t, block, "and it is an answer, so the block is filled rather than omitted")

	graphs, present := (*block)["acme-tracker"]
	assert.True(t, present,
		"the declared family must be a PRESENT key. A missing key is what a module receives when "+
			"it did not ask, so collapsing the two makes an empty store indistinguishable from a "+
			"dropped declaration")
	assert.Empty(t, graphs, "and it carries no graphs, because the store held none")
	assert.NotNil(t, graphs,
		"an EMPTY SLICE rather than nil: the three doc blocks that state this contract all say "+
			"empty slice, and nil round-trips through JSON as null rather than []")
}
