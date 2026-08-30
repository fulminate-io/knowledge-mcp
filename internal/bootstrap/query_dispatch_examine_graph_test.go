// SPDX-License-Identifier: Apache-2.0

package bootstrap

// query_dispatch_examine_graph_test.go pins both halves of examine's graph gate
// against the REAL intercept chain, which is the only place either is
// observable: the arm's decline used to reach engine.Dispatch's generic deny,
// and the refusal that replaced it is a CLAIM, so it changes what the chain
// answers and could in principle change WHICH arm answers.
//
// The second test is the control for exactly that risk. A refusal placed ahead
// of InterceptQueryExamineProjects would break project examine rather than
// improve a message, so a project-domain node on the knowledge graph is driven
// end-to-end and observed to still land on the project arm.

import (
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// examineRefusalLead is the opening of examineGraphRefusal
// (cmd/knowledge/internal/tools/intercept_query_examine.go). Matching on the
// lead plus the echoed graph name is what distinguishes this arm's refusal from
// any other arm's error text.
const examineRefusalLead = "examine: graph "

// TestQueryDispatchParity_ExamineNonKnowledgeGraphIsRefusedByName drives the
// graphs that actually REACH the examine arm. cloud, cicd, linkage, code and
// logs are deliberately absent from the list: each is claimed by its own arm
// earlier in bootstrap's chain, so an examine naming one of them never arrives
// at examine's gate at all — the companion subtest below asserts that rather
// than assuming it.
func TestQueryDispatchParity_ExamineNonKnowledgeGraphIsRefusedByName(t *testing.T) {
	for _, graph := range []string{"practice", "web", "pdf", "bogusgraph"} {
		t.Run(graph, func(t *testing.T) {
			c, eng := newParityClient(t)
			got := driveQueryParity(t, c, eng, map[string]any{
				"mode": "examine", "id": "n1", "graph": graph,
			})

			assert.Truef(t, got.handled,
				"examine on graph %q must be CLAIMED and refused by name, never declined into the "+
					"engine. Observed: %s", graph, got.body)
			assert.NotContainsf(t, got.body, genericDenyMarker,
				"examine on graph %q must not reach the generic deny. Observed: %s", graph, got.body)
			assert.Containsf(t, got.body, examineRefusalLead+`"`+graph+`" is not supported`,
				"the refusal must name the graph the caller sent. Observed: %s", got.body)
			assert.Containsf(t, got.body, `"graph":"`+graph+`"`,
				"the refusal must name the by-id alternative FOR THAT GRAPH, not a generic one. "+
					"Observed: %s", got.body)
			assert.Containsf(t, got.body, `"knowledge"`,
				"the refusal must name the surface examine DOES serve. Observed: %s", got.body)
			assert.Zerof(t, got.execDelta,
				"the refusal is a pre-read gate: %q must cost no Execute RPC", graph)
		})
	}

	// THE KNOWN POSITIVE. Every assertion above would also pass if the arm had
	// been changed to refuse EVERY examine call, so the graphs examine serves are
	// driven in the same run: they must carry no refusal and must reach the
	// composition, which against this fixture means Execute RPCs actually issued.
	for _, args := range []map[string]any{
		{"mode": "examine", "id": "n1"},
		{"mode": "examine", "id": "n1", "graph": "knowledge"},
	} {
		t.Run("served/"+argsLabel(args), func(t *testing.T) {
			c, eng := newParityClient(t)
			got := driveQueryParity(t, c, eng, args)

			assert.NotContainsf(t, got.body, examineRefusalLead,
				"the graph gate must not refuse the knowledge graph. Observed: %s", got.body)
			assert.Positivef(t, got.execDelta,
				"a knowledge examine must reach the composition's reads, not a gate. Observed: %s",
				got.body)
		})
	}
}

// argsLabel names a subtest after the graph value under drive.
func argsLabel(args map[string]any) string {
	if g, ok := args["graph"].(string); ok {
		return g
	}
	return "unset"
}

// TestQueryDispatchParity_ExamineProjectDomainStillWins is the chain-order
// control the refusal needs. InterceptQueryExamineProjects runs BEFORE
// InterceptQueryExamine so project-domain types get the richer project view;
// turning the general arm's decline into a claim is exactly the kind of change
// that can reorder a chain, and a project examine answered by the general arm
// would be a silent downgrade rather than a failure.
//
// THE DISCRIMINATOR IS THE truncated KEY. The general arm's JSON
// (buildInspectJSON) emits "truncated" unconditionally; the project arm's
// (buildExamineJSON) has no such key. Its absence is therefore the positive
// artifact that the project arm answered — and the second subtest proves the
// discriminator can fire by driving a NON-project type through the same fixture
// and observing the key appear.
func TestQueryDispatchParity_ExamineProjectDomainStillWins(t *testing.T) {
	t.Run("project_type_lands_on_the_project_arm", func(t *testing.T) {
		c, eng := newParityClient(t)
		eng.nodes.Store(&[]*knowledgev1.Node{{Id: "n1", Type: "project", SymbolName: "Proj"}})

		got := driveQueryParity(t, c, eng, map[string]any{
			"mode": "examine", "id": "n1", "format": "json",
		})

		assert.Truef(t, got.handled, "a project examine must be claimed. Observed: %s", got.body)
		assert.NotContainsf(t, got.body, examineRefusalLead,
			"the graph refusal must not shadow the project arm. Observed: %s", got.body)
		assert.NotContainsf(t, got.body, `"truncated"`,
			"a project-domain examine must be answered by InterceptQueryExamineProjects, whose JSON "+
				"carries no truncated key — this key means the GENERAL arm claimed it first. "+
				"Observed: %s", got.body)
		assert.Containsf(t, got.body, `"ancestry"`,
			"and it must be a real examine payload. Observed: %s", got.body)
	})

	t.Run("non_project_type_lands_on_the_general_arm", func(t *testing.T) {
		c, eng := newParityClient(t)
		eng.nodes.Store(&[]*knowledgev1.Node{{Id: "n1", Type: "thought", SymbolName: "Thought"}})

		got := driveQueryParity(t, c, eng, map[string]any{
			"mode": "examine", "id": "n1", "format": "json",
		})

		assert.Truef(t, got.handled, "a thought examine must be claimed. Observed: %s", got.body)
		assert.Containsf(t, got.body, `"truncated"`,
			"the general arm answers non-project types, and its JSON carries the truncated key — "+
				"without this the absence asserted above would prove nothing. Observed: %s", got.body)
	})
}
