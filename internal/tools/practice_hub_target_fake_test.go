// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_target_fake_test.go holds the seeded graph every hub row in this
// package drives against. It is a sibling of practice_hub_targets_test.go, which
// sits against the repo's per-file length ceiling, on that file's own precedent
// for a harness split: the SEED is what the rows share, and a reader looking for
// what a row asserts should not have to scroll past it.

import (
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// practiceHubTargetFake seeds the combined practice graph with every membership
// shape a target row needs and NOTHING in knowledge, so a by-ids read that
// addressed the wrong graph returns empty rather than quietly succeeding.
func practiceHubTargetFake(t *testing.T) *fakeGraphCaller {
	t.Helper()
	return &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{"knowledge": {}},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "practice", Name: workingset.DefaultInstanceName}: {
				hubTargetMemberA:  practiceHubNodeResult(t, hubTargetMemberA, "pattern", hubEndpointsHubA),
				hubTargetMemberB:  practiceHubNodeResult(t, hubTargetMemberB, "use_case", hubEndpointsHubA),
				hubTargetOtherHub: practiceHubNodeResult(t, hubTargetOtherHub, "use_case", hubEndpointsHubB),
				hubTargetNoKey:    practiceHubNodeResult(t, hubTargetNoKey, "use_case", ""),
				// PRESENT AND EMPTY, which practiceHubNodeResult cannot express: it
				// maps a hub of "" onto an empty metadata map, so the key-present
				// state needs its own seeding shape.
				hubTargetBlankKey: nodeResultJSON(t, hubTargetBlankKey, "use_case",
					map[string]string{kgtypes.MetaKeySourceHub: ""}),
				hubTargetTomb: practiceHubNodeResult(t, hubTargetTomb, "use_case", hubEndpointsHubA),
				// THE HUBS THEMSELVES, which no fake seeded until the resolution
				// gate existed. A hub id that resolves to nothing is now refused on
				// every arm, so a harness that seeded only MEMBERS would refuse
				// every hub-scoped row in this package and prove nothing about the
				// gate under test. hubTombstone is a hub that WAS one and was
				// deleted; hubGhost is deliberately absent, and is the shape the
				// resolution rows drive.
				hubEndpointsHubA: nodeResultJSON(t, hubEndpointsHubA, string(kgtypes.NodeSource), nil),
				hubEndpointsHubB: nodeResultJSON(t, hubEndpointsHubB, string(kgtypes.NodeSource), nil),
				hubTombstone:     nodeResultJSON(t, hubTombstone, string(kgtypes.NodeSource), nil),
				// A hub that is itself grouped under hub A — the nesting shape.
				hubNestedHub: nodeResultJSON(t, hubNestedHub, string(kgtypes.NodeSource),
					map[string]string{kgtypes.MetaKeySourceHub: hubEndpointsHubA}),
			},
		},
		tombstonedIDs: map[string]bool{hubTargetTomb: true, hubTombstone: true},
		// THE GRAPH'S OWN EDGE VOCABULARY, which the link arm resolves a caller's
		// spelling against. Without it every relationship is admitted as a NEW
		// family and a case variant never becomes the canonical spelling — so the
		// refusal that runs on the CANONICALISED relationship could not be driven
		// at all, and a row for it would pass on a resolve that did nothing.
		statsResp: &knowledgev1.GraphStats{
			EdgesByType: map[string]int64{
				string(kgtypes.EdgeSourcedFrom): 1, "contains": 1, "relates-to": 1,
			},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"practice", "default"}),
	}
}
