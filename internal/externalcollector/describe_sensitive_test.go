// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// describe_sensitive_test.go — the EMPTY-SENSITIVE MARK crossing the wire.
//
// THE PROPERTY MUST BE LEGAL ON BOTH GATES OR ON NEITHER. A declaration is
// checked twice: against the checked-in JSON Schema, and by a strict decode that
// refuses a field this client does not define. A property added to the schema
// and not to the Go struct is refused by the decode; one added to the struct and
// not to the schema would be admitted here and refused by a port that validates
// against its own copy. So the row below drives BOTH directions through the real
// DecodeDeclaration rather than either gate alone.

// TestDecodeDeclaration_CarriesTheEmptySensitiveMark is the positive: a provider
// that marks a name has that mark reach the decoded declaration.
func TestDecodeDeclaration_CarriesTheEmptySensitiveMark(t *testing.T) {
	decl, err := DecodeDeclaration("describe", json.RawMessage(`{
	  "behavior":{"summarizable":false,"embeddable":false,"syncable":true},
	  "node_types":[],"edge_types":[],
	  "environment":[
	    {"name":"LOKI_PASSWORD","class":"secret","empty_sensitive":true},
	    {"name":"KUBERNETES_SERVICE_HOST","class":"not-carried","empty_sensitive":true},
	    {"name":"HOME","class":"path"}
	  ]}`))
	require.NoError(t, err)
	require.Len(t, decl.Environment, 3)
	assert.True(t, decl.Environment[0].EmptySensitive, "a marked secret-class name lost its mark")
	// THE not-carried ROW IS THE ONE THAT MATTERS. That class is dropped from the
	// installer's entry-writing table by construction, so this is the only path a
	// mark on it has; a client that admitted the mark only on carried classes
	// would silently drop the one shipped collector's marked name.
	assert.True(t, decl.Environment[1].EmptySensitive, "a marked not-carried name lost its mark")
	assert.False(t, decl.Environment[2].EmptySensitive, "an unmarked name arrived marked")
}

// TestDecodeDeclaration_AnOmittedMarkDecodesFalse pins ABSENT MEANS FALSE, which
// is what makes every collector that predates the property, and every collector
// that discriminates on nothing, byte-identical to what it was.
func TestDecodeDeclaration_AnOmittedMarkDecodesFalse(t *testing.T) {
	decl, err := DecodeDeclaration("describe", json.RawMessage(`{
	  "behavior":{"summarizable":false,"embeddable":false,"syncable":true},
	  "node_types":[],"edge_types":[],
	  "environment":[{"name":"HOME","class":"path"}]}`))
	require.NoError(t, err)
	require.Len(t, decl.Environment, 1)
	assert.False(t, decl.Environment[0].EmptySensitive)
	// The explicit false is the same statement as the omission, and it round-trips
	// to an omission because the field carries omitempty: a declaration that says
	// false out loud writes no more bytes than one that says nothing.
	raw, err := json.Marshal(decl.Environment[0])
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "empty_sensitive")
}

// TestDecodeDeclaration_RefusesAMisspelledMark is the control the positive rows
// need: the strict decode is still strict, so "the property is accepted" is a
// statement about THIS key rather than about the decoder having gone quiet.
func TestDecodeDeclaration_RefusesAMisspelledMark(t *testing.T) {
	_, err := DecodeDeclaration("describe", json.RawMessage(`{
	  "behavior":{"summarizable":false,"embeddable":false,"syncable":true},
	  "node_types":[],"edge_types":[],
	  "environment":[{"name":"HOME","class":"path","empty_sensitve":true}]}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty_sensitve")
}

// TestDescribeContract_DeclaresTheEmptySensitiveProperty reads the CHECKED-IN
// schema rather than the decoded value, because the schema is what every port
// validates against and what a non-Go collector author reads. A property the Go
// struct carries and the schema does not would pass every row above and refuse a
// Python or TypeScript collector that emitted it.
func TestDescribeContract_DeclaresTheEmptySensitiveProperty(t *testing.T) {
	var doc struct {
		Properties struct {
			Environment struct {
				Items struct {
					Required   []string                  `json:"required"`
					Properties map[string]map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"environment"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(DescribeContractJSON(), &doc))
	prop, ok := doc.Properties.Environment.Items.Properties["empty_sensitive"]
	require.True(t, ok, "the checked-in describe schema declares no empty_sensitive property on an environment entry")
	assert.Equal(t, "boolean", prop["type"])
	// IT IS OPTIONAL, and that is the whole compatibility story: every collector
	// written before the property existed still satisfies the schema.
	assert.NotContains(t, doc.Properties.Environment.Items.Required, "empty_sensitive")
	// The control, same run and same read: the two properties that ARE required
	// are still required, so a schema that lost its `required` list entirely would
	// not satisfy the row above.
	assert.ElementsMatch(t, []string{"name", "class"}, doc.Properties.Environment.Items.Required)
}
