// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// context_edge_coherence_test.go — A DECLARATION THAT COULD ONLY EVER RECEIVE
// ZERO EDGES IS REFUSED, RATHER THAN FILLED AND RETURNED EMPTY.
//
// THE DEFECT THIS CLOSES. collect_context_project.go's contextGraphEdges doc
// claimed the combination "edge_fields with no node_types" was "already refused
// upstream as an incoherent declaration". It was not: the incoherence condition
// named node fields and metadata keys and omitted edge_fields, so
// {"aws":{"edge_fields":["from_id"]}} validated, filled, and returned a graph
// with no edges and no error. A caller reads that as "this family has no
// dependencies" when the truth is "you did not ask in a way that could answer".
//
// TWO SHAPES PRODUCE THE SAME SILENT ZERO and both are refused, because either
// alone is sufficient to cause it:
//   - no node_types: nothing is carried, so no id enters the edge pivot set;
//   - node_types but no `id` node field: nodes are carried with empty ids, and
//     the pivot walk skips a node whose id is empty.
//
// THE REFUSAL NAMES THE FIELD AND THE FIX, because a declaration is something an
// operator writes by hand into a config file and a refusal that only says
// "invalid" sends them to read source.
func TestValidate_EdgeFieldsWithoutNodeTypesIsRefused(t *testing.T) {
	decl := ContextDeclaration{"aws": {EdgeFields: []string{"from_id", "to_id"}}}
	err := decl.Validate()
	require.Error(t, err,
		"a family declaring edge_fields with no node_types can only ever receive zero edges; "+
			"filling it and returning an empty slice reports absence where the question was malformed")
	assert.Contains(t, err.Error(), "edge_fields")
	assert.Contains(t, err.Error(), "node_types", "the refusal names the field that would fix it")
	assert.Contains(t, err.Error(), "aws", "and the family it rejected")
}

func TestValidate_EdgeFieldsWithoutTheIDNodeFieldIsRefused(t *testing.T) {
	decl := ContextDeclaration{"aws": {
		NodeTypes:  []string{"aws-resource"},
		NodeFields: []string{ContextNodeFieldSymbolName},
		EdgeFields: []string{"from_id"},
	}}
	err := decl.Validate()
	require.Error(t, err,
		"the edge read pivots on the carried nodes' ids, so a declaration that does not ask for "+
			"the id field receives nodes with empty ids and zero edges")
	assert.Contains(t, err.Error(), "edge_fields")
	assert.Contains(t, err.Error(), ContextNodeFieldID, "the refusal names the field that would fix it")
}

// TestValidate_ACoherentEdgeDeclarationIsAccepted is the CONTROL, and it is what
// keeps the two refusals above from being satisfied by a validator that rejects
// every edge declaration.
func TestValidate_ACoherentEdgeDeclarationIsAccepted(t *testing.T) {
	decl := ContextDeclaration{"aws": {
		NodeTypes:  []string{"aws-resource"},
		NodeFields: []string{ContextNodeFieldID, ContextNodeFieldSymbolName},
		EdgeFields: []string{"from_id", "to_id"},
	}}
	assert.NoError(t, decl.Validate(),
		"control: node_types plus the id field plus edge_fields is the shape that CAN answer, and "+
			"it must still validate")

	// AND THE OTHER CONTROL: a declaration with no edge_fields at all is
	// untouched by the new legs, so they are a statement about edge declarations
	// rather than a new requirement on every family.
	assert.NoError(t, ContextDeclaration{"aws": {
		NodeTypes:  []string{"aws-resource"},
		NodeFields: []string{ContextNodeFieldSymbolName},
	}}.Validate(),
		"control: a family that asks for no edges owes no id field")
}
