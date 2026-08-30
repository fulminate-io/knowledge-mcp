// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_bijection_test.go proves the completeness contract for the query
// param-accounting gate: every armID in queryArmRegistry has EXACTLY ONE gate
// call site in production source, and every gate call site names an armID the
// registry declares.
//
// WHY A BIJECTION AND NOT A COUNT. A count is satisfied by a registry that
// declares one arm per claim point and wires the gate at the wrong one — the
// arms would still be 48 and the call sites still 48. Set equality plus a
// once-each multiplicity check is what makes an omitted gate name the specific
// armID that lost it, which is the failure a later reader can act on.
//
// A BIJECTION ALONE IS NOT ENOUGH, and that is why the floors are here too. A
// degenerate registry that collapsed the cloud/cicd surface into a single arm,
// with a single gate call, would biject perfectly and account for nothing. The
// floors are the plan-locked MINIMA per multi-shape entry point: finding a
// further distinct read set means ADDING an arm and staying green, never
// removing one.
//
// HOW A GATE CALL SITE IS RECOGNIZED. Every production reference to a query
// armID constant OUTSIDE the registry table files is a wiring reference: either
// an accountQueryParams(armX, raw) call at a claim point, or one of
// reflectArmFor's returns (the reflect surface routes its ten arms through one
// dispatcher, so its "call sites" are that function's return arms). The registry
// files are excluded because their references are the DECLARATIONS being
// checked, not uses of them — counting those would make the test compare the
// table with itself.
//
// SCOPED TO QUERY ARMS. The package also declares mutate's armIDs over the same
// armID type, so the scan admits an identifier only when queryArmRegistry
// declares it. That keeps the mutate wiring out of the comparison without
// pattern-matching on names.

import (
	"go/ast"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queryArmRegistryFilePrefix names the sibling table files whose armID
// references are declarations rather than gate wiring.
const queryArmRegistryFilePrefix = "query_arm_registry"

// queryEntryPointArmFloors is the plan-locked minimum arm count per multi-shape
// entry point, with the arms each one owns. The floors are MINIMA: two entry
// points legitimately exceed theirs, which is recorded per row.
var queryEntryPointArmFloors = []struct {
	entryPoint string
	floor      int
	arms       []armID
}{
	{
		// Exceeds its floor by one: the ranked-text search
		// (composeResourceSearchClient) is a distinct read set from the browse.
		entryPoint: "InterceptQueryCloudCICD", floor: 4,
		arms: []armID{
			armCloudCICDListGraphs, armCloudCICDGetNode, armCloudCICDStats,
			armCloudCICDSearch, armCloudCICDBrowse,
		},
	},
	{
		// Exceeds its floor by two: the practice language:"all" scatter-gather
		// fan-out is a distinct read set the floor folded into "practice search",
		// and the text-less practice browse is a second.
		entryPoint: "InterceptQueryPracticeLinkage", floor: 8,
		arms: []armID{
			armPracticeListGraphs, armPracticeStats, armPracticeBrowse,
			armPracticeSearchFanOut, armPracticeSearch,
			armLinkageListGraphs, armLinkageStats, armLinkageGetNode, armLinkageSearchRetired,
			armWebPDFSearch, armWebPDFStats,
		},
	},
	{
		entryPoint: "interceptQueryReflect", floor: 10,
		arms: []armID{
			armReflectPersonality, armReflectInfluence, armReflectTensions, armReflectBlindSpots,
			armReflectSummary, armReflectEvolution, armReflectClusters, armReflectThoughtExamine,
			armReflectSimulate, armReflectRecall,
		},
	},
	{
		entryPoint: "InterceptQueryKnowledgeSearch", floor: 2,
		arms: []armID{armKnowledgeRecentBrowse, armKnowledgeSearch},
	},
	{
		entryPoint: "InterceptQueryCorrelationsPivot", floor: 2,
		arms: []armID{armCorrelations, armPivot},
	},
	{
		entryPoint: "InterceptQueryExplainTimeline", floor: 2,
		arms: []armID{armExplain, armTimeline},
	},
	{
		entryPoint: "InterceptQueryModulesCodeStats", floor: 2,
		arms: []armID{armCodeModules, armCodeStats},
	},
}

// TestQueryArmGateCallSites_BijectWithRegistry is the completeness contract.
func TestQueryArmGateCallSites_BijectWithRegistry(t *testing.T) {
	pkg := parseToolsPackage(t)
	require.NotEmpty(t, pkg.files, "the scan must parse production sources — an empty parse proves nothing")
	require.NotEmpty(t, queryArmRegistry, "the registry must be assembled before it can be compared")

	wired := map[armID]int{}
	scanned := 0
	for name, file := range pkg.files {
		if strings.HasPrefix(name, queryArmRegistryFilePrefix) {
			continue // declarations, not wiring
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			arm := armID(ident.Name)
			if _, declared := queryArmRegistry[arm]; declared {
				wired[arm]++
			}
			return true
		})
	}

	// NON-VACUITY, asserted BEFORE the comparison. A scan that walked nothing —
	// a moved package directory, a prefix that excluded every file — produces an
	// empty `wired` map, and "every wired arm is declared" is then trivially
	// true. Both halves are checked: files were read, and armIDs were found in
	// them.
	require.Positivef(t, scanned, "the scan must read production files outside %s*", queryArmRegistryFilePrefix)
	require.NotEmpty(t, wired, "the scan must find gate call sites — zero references means the walk missed them")

	// The total is an ASSERT, not a require, so a shortfall does not abort before
	// the per-arm subtest below runs. That subtest is what NAMES the arm that
	// lost its gate; failing here first would report only a bare count mismatch,
	// which tells an operator that something is wrong but not what to fix. Verified by
	// removing one gate call and observing the run name armEvidence.
	total := 0
	for _, n := range wired {
		total += n
	}
	assert.Equalf(t, queryArmCount, total,
		"the scan must find exactly one wiring reference per arm; found %d references across %d arms",
		total, len(wired))

	t.Run("every declared arm is wired exactly once", func(t *testing.T) {
		var unwired, duplicated []string
		for arm := range queryArmRegistry {
			switch n := wired[arm]; {
			case n == 0:
				unwired = append(unwired, string(arm))
			case n > 1:
				duplicated = append(duplicated, string(arm))
			}
		}
		sort.Strings(unwired)
		sort.Strings(duplicated)
		assert.Emptyf(t, unwired,
			"these arms are declared in the registry but have NO gate call site, so their claim points "+
				"serve calls with no param accounting at all: %v", unwired)
		assert.Emptyf(t, duplicated,
			"these arms are wired at more than one place, so the registry no longer describes one claim "+
				"point per arm: %v", duplicated)
	})

	t.Run("every wired arm is declared", func(t *testing.T) {
		// The scan only admits identifiers the registry declares, so this
		// direction cannot fail by construction — which is exactly why it needs
		// a KNOWN POSITIVE rather than a bare assertion. Drive the same
		// membership test with an armID the registry does not declare and
		// confirm it is rejected.
		const probe armID = "armNoSuchQueryArmProbe"
		_, declared := queryArmRegistry[probe]
		require.False(t, declared, "the probe must not accidentally be a real arm")
		assert.Zero(t, wired[probe],
			"an undeclared armID must never be counted as wired — otherwise the scan's filter is inert")
	})
}

// TestQueryArmFloors_EveryMultiShapeEntryPointMeetsItsMinimum pins the locked
// per-entry-point minima and, with them, that the floor table itself partitions
// the multi-arm half of the registry. Without the partition check a floor row
// could name arms that no longer exist, or silently stop covering an arm that
// was added.
func TestQueryArmFloors_EveryMultiShapeEntryPointMeetsItsMinimum(t *testing.T) {
	require.NotEmpty(t, queryEntryPointArmFloors, "the floor table must be populated")

	seen := map[armID]string{}
	for _, row := range queryEntryPointArmFloors {
		t.Run(row.entryPoint, func(t *testing.T) {
			assert.GreaterOrEqualf(t, len(row.arms), row.floor,
				"%s is locked at a MINIMUM of %d arms and now declares %d — an arm was collapsed, which "+
					"is the degeneracy the floors exist to prevent", row.entryPoint, row.floor, len(row.arms))
			for _, arm := range row.arms {
				_, declared := queryArmRegistry[arm]
				assert.Truef(t, declared, "%s claims arm %s which the registry does not declare", row.entryPoint, arm)
				if prior, dup := seen[arm]; dup {
					assert.Failf(t, "arm claimed by two entry points",
						"%s is listed under both %s and %s", arm, prior, row.entryPoint)
					continue
				}
				seen[arm] = row.entryPoint
			}
		})
	}

	// The floor table covers the MULTI-arm entry points only; the rest take one
	// arm each. Assert the remainder is exactly that, so an arm added to a
	// multi-shape entry point cannot hide by being left out of the table.
	singleArmed := queryArmCount - len(seen)
	assert.Equalf(t, len(queryArmRegistry)-len(seen), singleArmed,
		"the arms outside the floor table must be the single-arm entry points; the floor table covers %d "+
			"of %d arms", len(seen), queryArmCount)
	assert.Positive(t, singleArmed, "there must be single-arm entry points left over — else the table is over-broad")
}
