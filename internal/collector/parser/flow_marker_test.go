// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// langNodeOf returns the per-language hub node a populate run created for one
// language, or nil.
func langNodeOf(res PopulateResult, id string) *knowledgev1.Node {
	for _, n := range res.Nodes {
		if n.Id == id {
			return n
		}
	}
	return nil
}

// TestFlowCapabilityMarker pins the THREE-WAY answer a consumer needs: a marked
// hub, an unmarked hub, and no hub at all. The distinction between the first two
// is what lets a scan tell "no taint" from "no facts collected".
//
// PREREQUISITE: at least one REGISTERED flow arm. The registry itself registers
// nothing; the Go arm's init() is the first registration. If
// armed_language_carries_marker and the "go" control inside
// unarmed_language_has_no_key are the two things red, check that
// chunker_go_flowsteps.go exists and its init() calls RegisterGoFlowSteps before
// debugging anything here.
func TestFlowCapabilityMarker(t *testing.T) {
	t.Run("armed_language_carries_marker", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{{path: "svc/marked.go", src: `package svc

func Handle(p string) string { return p }
`}})
		hub := langNodeOf(res, "lang:testrepo:go")
		require.NotNil(t, hub, "control: the populate run created a per-language hub node for go")
		assert.Equal(t, "1", hub.Metadata["flow_arm"],
			"go has a registered flow arm, so its hub node carries the marker")
	})

	t.Run("unarmed_language_has_no_key", func(t *testing.T) {
		// THE UNARMED LANGUAGE IS PICKED AT RUNTIME FROM THE EXPORTED REGISTRY,
		// never from a table and never hardcoded. That is what keeps this subtest
		// from going red the day that language gains an arm — it simply picks a
		// different one.
		var unarmed treesitter.Language
		for _, l := range treesitter.RegisteredLanguages() {
			if _, ok := treesitter.FlowStepsArm(l); !ok {
				unarmed = l
				break
			}
		}
		require.NotEmpty(t, string(unarmed),
			"every registered language is armed, so this subtest has no subject — that is a real "+
				"failure of its premise, not a condition to skip over")

		// ensureLangNode is DRIVEN DIRECTLY rather than through a fixture file: a
		// fixture needs source in a specific language and a matching extension,
		// which a language chosen at runtime cannot supply. This is the exact
		// function the marker is stamped in.
		langNodes := map[string]string{}
		var nodes []*knowledgev1.Node
		ensureLangNode("testrepo", string(unarmed), langNodes, &nodes)
		require.Len(t, nodes, 1)
		_, present := nodes[0].Metadata["flow_arm"]
		assert.False(t, present,
			"THE KEY IS ABSENT, not empty: %s has no flow arm, and an empty value would collapse "+
				"'collected without an arm' into 'collected with one that found nothing'", unarmed)

		// KNOWN-POSITIVE CONTROL: the same function, one call later, DOES set the
		// key. Without it, a key absent because the code never sets one for
		// anybody would read as a pass.
		langNodes = map[string]string{}
		nodes = nil
		ensureLangNode("testrepo", "go", langNodes, &nodes)
		require.Len(t, nodes, 1)
		_, goPresent := nodes[0].Metadata["flow_arm"]
		assert.True(t, goPresent,
			"control: the same call path DOES stamp the key for an armed language")
	})

	t.Run("marker_is_versioned", func(t *testing.T) {
		// THE VALUE IS A VERSION, NOT A BOOLEAN, and this pins it so a later
		// reader does not collapse it into one. A change to what an arm computes
		// bumps this to "2" and a consumer can refuse semantics it does not know.
		langNodes := map[string]string{}
		var nodes []*knowledgev1.Node
		ensureLangNode("testrepo", "go", langNodes, &nodes)
		require.Len(t, nodes, 1)
		assert.Equal(t, "1", nodes[0].Metadata["flow_arm"])
		assert.NotEqual(t, "true", nodes[0].Metadata["flow_arm"],
			"a boolean would force a second key the first time the semantics changed")
	})
}
