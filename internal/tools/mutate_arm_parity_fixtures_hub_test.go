// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_arm_parity_fixtures_hub_test.go holds the PRACTICE-HUB seeding the parity
// fixtures drive their source_hub rows against. Split out of
// mutate_arm_parity_fixtures_test.go, which sits against the repo's per-file
// length ceiling, on that file's own precedent for the harness split.
//
// The hub rows need seeding the flat fixture map cannot express: a node must
// resolve in the COMBINED PRACTICE GRAPH and NOT in knowledge, because which
// graph an endpoint or target resolves in is half of what the hub gates decide.

import (
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// parityProbeHub is the hub id the source_hub row drives, and it is the literal
// parityProbe hands a plain string param — so the seeded endpoints below carry
// the very value the probe sends rather than a value chosen to agree with it.
const parityProbeHub = "probe-source_hub"

// seedParityHubMembers puts the two hub endpoints in the COMBINED PRACTICE GRAPH
// and nothing in knowledge, which is what routes the link to the practice arm:
// the FROM-first probe must miss in knowledge for the composer to treat the call
// as cross-graph at all.
func seedParityHubMembers(fc *fakeGraphCaller) {
	fc.queryResponsesByGraph = map[string]map[string]kgtools.ToolResult{"knowledge": {}}
	fc.queryResponsesByGraphName = map[graphKey]map[string]kgtools.ToolResult{
		{Type: "practice", Name: workingset.DefaultInstanceName}: {
			paritySeedHubA: parityHubNode(paritySeedHubA),
			paritySeedHubB: parityHubNode(paritySeedHubB),
			// AND THE HUB ITSELF. A `source_hub` that resolves to no node is now
			// refused on every arm, so a fixture seeding only the MEMBERS would
			// have every hub row report the resolution refusal instead of the
			// param classification it exists to measure.
			parityProbeHub: paritySourceHubNode(parityProbeHub),
		},
	}
}

// paritySeedPassthroughMember is the practice node the passthrough arm's UPDATE
// rows write to. It is grouped under the hub that arm's base payload names, so a
// row driven in the update shape reaches the write rather than the hub refusal.
const paritySeedPassthroughMember = "parity-passthrough-member"

// seedParityPassthroughHubMember puts that member in the COMBINED PRACTICE GRAPH
// under hub-1 — the hub armGraphPassthrough's base payload carries — and nothing
// in knowledge, matching seedParityHubMembers' rule that a practice row must miss
// in knowledge to be routed as one.
func seedParityPassthroughHubMember(fc *fakeGraphCaller) {
	fc.queryResponsesByGraph = map[string]map[string]kgtools.ToolResult{"knowledge": {}}
	fc.queryResponsesByGraphName = map[graphKey]map[string]kgtools.ToolResult{
		{Type: "practice", Name: workingset.DefaultInstanceName}: {
			paritySeedPassthroughMember: {Content: []kgtools.ContentBlock{{Type: "text",
				Text: `{"id":"` + paritySeedPassthroughMember +
					`","type":"pattern","metadata":{"source_hub":"hub-1"}}`}}},
			// The hub that member is grouped under, for seedParityHubMembers' reason.
			parityPassthroughHub: paritySourceHubNode(parityPassthroughHub),
		},
	}
}

// parityPassthroughHub is the hub armGraphPassthrough's base payload names.
const parityPassthroughHub = "hub-1"

// paritySourceHubNode renders one HUB node — type source, carrying no hub of its
// own, which is what the resolution requires of a hub.
func paritySourceHubNode(id string) kgtools.ToolResult {
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text",
		Text: `{"id":"` + id + `","type":"source"}`}}}
}

// parityHubNode renders one practice node grouped under parityProbeHub.
func parityHubNode(id string) kgtools.ToolResult {
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{"id":"` + id +
		`","type":"pattern","metadata":{"source_hub":"` + parityProbeHub + `"}}`}}}
}
