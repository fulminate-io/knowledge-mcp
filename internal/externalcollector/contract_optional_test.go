// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contract_optional_test.go — the REQUIRED / OPTIONAL distinction in the schema
// gate, and the input contract's optional context property.
//
// WHY THE DISTINCTION HAD TO BE DRAWN. The gate walks every property the
// contract declares and refuses an advertised schema lacking it. Adding the
// context block as a declared property under that rule would refuse every
// PRE-EXISTING third-party provider — a provider that never heard of the
// property, advertising a hand-written {id, params} schema, told to fix a line it
// never wrote. The block is therefore an OPTIONAL property and the gate learns
// the difference: a provider lacking an optional property is admitted unchanged
// and receives no block.
//
// THE DISCRIMINATOR IS THE CONTRACT'S OWN REQUIRED LIST, never the advertised
// one. That is the arm the third row below exists to pin: a provider that lists
// `id` as required and then declares no `id` property is still refused, and it is
// refused by the PROPERTY loop rather than the required-list loop.

// advertisedFromContract decodes the checked-in input contract into the mutable
// document shape an advertised schema takes, so a row can bend exactly one thing
// about a document that is otherwise the contract verbatim.
func advertisedFromContract(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	return doc
}

// dropProperty removes one property from an advertised schema document.
func dropProperty(doc map[string]any, name string) map[string]any {
	delete(doc["properties"].(map[string]any), name)
	return doc
}

// TestInputContract_DeclaresContextAsAnOptionalProperty pins both halves of the
// placement in one row: the block IS in the published schema a collector author
// reads, and it is NOT on the required list.
func TestInputContract_DeclaresContextAsAnOptionalProperty(t *testing.T) {
	var schema map[string]any
	require.NoError(t, json.Unmarshal(InputContractJSON(), &schema))

	assert.ElementsMatch(t, []any{"id"}, schema["required"],
		"the collect id is the ONLY required input property; an added required property refuses every existing provider")

	props := schema["properties"].(map[string]any)
	ctxProp, ok := props["context"].(map[string]any)
	require.True(t, ok, "the context block is a declared property of the published input schema")
	assert.Equal(t, "object", ctxProp["type"])

	// THE CONTROL: the two properties that were there before are untouched, so
	// the assertion above is about an addition rather than about a rewritten file.
	assert.Equal(t, "string", props["id"].(map[string]any)["type"])
	assert.Equal(t, "object", props["params"].(map[string]any)["type"])
}

// TestCheckToolSchemas_AdmitsAProviderLackingAnOptionalProperty is R7's core
// pair. Cell (i) and cell (ii) are the two properties the contract declares
// without requiring; both must be admitted when absent.
func TestCheckToolSchemas_AdmitsAProviderLackingAnOptionalProperty(t *testing.T) {
	out := advertisedFromContract(t, OutputContractJSON())

	t.Run("advertised schema lacks the optional context property", func(t *testing.T) {
		in := dropProperty(advertisedFromContract(t, InputContractJSON()), "context")
		assert.NoError(t, CheckToolSchemas("collect", in, out),
			"a provider written before this property existed is admitted unchanged")
	})

	t.Run("advertised schema lacks the optional params property", func(t *testing.T) {
		in := dropProperty(advertisedFromContract(t, InputContractJSON()), "params")
		assert.NoError(t, CheckToolSchemas("collect", in, out),
			"params is optional on the contract's own required list, so the same rule reaches it — "+
				"a deliberate widening, written down here rather than discovered")
	})

	// THE SAME-RUN CONTROL, through the same instrument and the same path: the
	// contract advertised verbatim passes, so the two admissions above are not a
	// gate that stopped checking.
	t.Run("control: the contract advertised verbatim passes", func(t *testing.T) {
		in := advertisedFromContract(t, InputContractJSON())
		assert.NoError(t, CheckToolSchemas("collect", in, out))
	})
}

// TestCheckToolSchemas_StillRefusesAMissingRequiredProperty is the arm the
// widening must not take with it, and it names WHICH loop keeps it: the
// advertised required list still contains "id", so the required-list loop does
// not fire and the refusal comes from the property loop reading the CONTRACT's
// required list.
func TestCheckToolSchemas_StillRefusesAMissingRequiredProperty(t *testing.T) {
	in := dropProperty(advertisedFromContract(t, InputContractJSON()), "id")
	require.Contains(t, in["required"], "id",
		"fixture control: the advertised required list still names id, so only the property is missing")

	err := CheckToolSchemas("collect", in, advertisedFromContract(t, OutputContractJSON()))
	require.Error(t, err, "id is on the CONTRACT's required list, so its property is not optional")
	assert.Contains(t, err.Error(), `the property "id"`)
}

// TestCheckToolSchemas_RefusesAnOptionalPropertyOfTheWrongType is cell (iv): the
// optional arm must skip an ABSENT property, never a PRESENT wrong one. Without
// this row a gate that returned early on the whole property would pass a
// provider declaring context as a string.
func TestCheckToolSchemas_RefusesAnOptionalPropertyOfTheWrongType(t *testing.T) {
	in := advertisedFromContract(t, InputContractJSON())
	in["properties"].(map[string]any)["context"] = map[string]any{"type": "string"}

	err := CheckToolSchemas("collect", in, advertisedFromContract(t, OutputContractJSON()))
	require.Error(t, err, "a declared optional property is still compared when it IS declared")
	assert.Contains(t, err.Error(), `inputSchema.context`)
	assert.Contains(t, err.Error(), `requires type "object"`)
}

// TestContractSummary_StatesTheContextBlock keeps the operator-facing one-liner
// honest with the file it summarizes. It is the line an operator reads in the
// registration tool's description without opening the schema.
func TestContractSummary_StatesTheContextBlock(t *testing.T) {
	assert.Contains(t, ContractSummary(), "context",
		"the summary names every input property the contract declares")
	// THE CONTROL: the two it already named are still there.
	assert.Contains(t, ContractSummary(), "id (string, required)")
	assert.Contains(t, ContractSummary(), "params")
}
