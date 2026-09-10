// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contract_test.go — the contract artifacts themselves: what the checked-in
// schemas require, what the comparator accepts and refuses, and that the Go
// envelope and the JSON schema still describe the same shape.

// TestOutputContract_RequiresTheCompletenessAssertion pins the requirement the
// deletion phase downstream depends on. It reads the CHECKED-IN artifact rather
// than a transcription of it, because the artifact is what a collector author
// copies into their tool.
func TestOutputContract_RequiresTheCompletenessAssertion(t *testing.T) {
	var schema map[string]any
	require.NoError(t, json.Unmarshal(OutputContractJSON(), &schema))

	assert.Equal(t, "object", schema["type"])
	assert.ElementsMatch(t, []any{"nodes", "edges", "walk_complete"}, schema["required"],
		"the completeness assertion is REQUIRED — a provider must not be able to omit it and disable deletion by silence")

	props := schema["properties"].(map[string]any)
	assert.Equal(t, "boolean", props["walk_complete"].(map[string]any)["type"])
	assert.Equal(t, "array", props["nodes"].(map[string]any)["type"])
	assert.Equal(t, "array", props["edges"].(map[string]any)["type"])
}

// TestInputContract_RequiresTheCollectID pins the input side: the collect id
// names the graph instance, so a tool that does not take it cannot be driven.
func TestInputContract_RequiresTheCollectID(t *testing.T) {
	var schema map[string]any
	require.NoError(t, json.Unmarshal(InputContractJSON(), &schema))
	assert.Equal(t, "object", schema["type"])
	assert.ElementsMatch(t, []any{"id"}, schema["required"])
	props := schema["properties"].(map[string]any)
	assert.Equal(t, "string", props["id"].(map[string]any)["type"])
	assert.Equal(t, "object", props["params"].(map[string]any)["type"])
}

// TestContractSchemaAndEnvelopeAgree is the drift guard between the two halves
// of one contract: the checked-in JSON a collector author reads, and the Go
// struct this client decodes into. A field added to one and not the other is a
// contract that says two different things.
func TestContractSchemaAndEnvelopeAgree(t *testing.T) {
	// Every key the schema names must decode into the envelope...
	minimal := `{"nodes":[{"id":"a","type":"issue"}],"edges":[{"from_id":"a","to_id":"b","type":"blocks"}],"walk_complete":true}`
	r, err := DecodeResult("t", json.RawMessage(minimal))
	require.NoError(t, err, "the schema's own minimal document must decode into the envelope")
	require.Len(t, r.Nodes, 1)
	require.Len(t, r.Edges, 1)
	assert.True(t, r.WalkComplete)

	// ...and the envelope must name no top-level field the schema does not.
	encoded, err := json.Marshal(&Result{})
	require.NoError(t, err)
	var asMap map[string]any
	require.NoError(t, json.Unmarshal(encoded, &asMap))
	var schema map[string]any
	require.NoError(t, json.Unmarshal(OutputContractJSON(), &schema))
	props := schema["properties"].(map[string]any)
	for key := range asMap {
		assert.Contains(t, props, key,
			"the envelope carries the top-level field %q and the checked-in contract schema does not describe it", key)
	}
}

// TestCheckToolSchemas_MissingSchemas pins the two absence arms at the unit
// level. The no-output-schema arm has an end-to-end sibling against a real
// provider; the no-INPUT-schema arm can only be reached here, because the SDK's
// server refuses to publish a tool with no input schema at all.
func TestCheckToolSchemas_MissingSchemas(t *testing.T) {
	err := CheckToolSchemas("collect_graph", nil, contractMap(t, OutputContractJSON()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NO input schema")
	assert.Contains(t, err.Error(), "collect_graph")

	err = CheckToolSchemas("collect_graph", contractMap(t, InputContractJSON()), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NO output schema")
	assert.Contains(t, err.Error(), "walk_complete")
}

// TestCheckToolSchemas_AcceptsTheContractVerbatimAndStricter pins what the
// comparator ADMITS, which is what keeps it from being a schema-equality check:
// a provider may be stricter than the contract, never looser.
func TestCheckToolSchemas_AcceptsTheContractVerbatimAndStricter(t *testing.T) {
	require.NoError(t, CheckToolSchemas("collect_graph",
		contractMap(t, InputContractJSON()), contractMap(t, OutputContractJSON())))

	stricter := contractMap(t, OutputContractJSON())
	stricter["required"] = []any{"nodes", "edges", "walk_complete", "collected_at"}
	props := stricter["properties"].(map[string]any)
	props["collected_at"] = map[string]any{"type": "string"}
	require.NoError(t, CheckToolSchemas("collect_graph", contractMap(t, InputContractJSON()), stricter),
		"a provider declaring MORE than the contract is conforming; the contract is a floor, not an equality")
}

// TestCheckToolSchemas_RefusesEachLooseningIndividually walks the ways an
// advertised schema can fall short. Each row bends ONE thing, so a comparator
// that stopped checking any single one of them turns a row red rather than
// leaving the suite green.
func TestCheckToolSchemas_RefusesEachLooseningIndividually(t *testing.T) {
	for _, tc := range []struct {
		name       string
		bend       func(out map[string]any)
		wantErrHas string
	}{
		{
			"top-level type is not an object",
			func(out map[string]any) { out["type"] = "array" },
			`the contract requires type "object"`,
		},
		{
			"walk_complete not required",
			func(out map[string]any) { out["required"] = []any{"nodes", "edges"} },
			"walk_complete",
		},
		{
			"walk_complete property missing",
			func(out map[string]any) { delete(out["properties"].(map[string]any), "walk_complete") },
			"walk_complete",
		},
		{
			"walk_complete declared as a string",
			func(out map[string]any) {
				out["properties"].(map[string]any)["walk_complete"] = map[string]any{"type": "string"}
			},
			`requires type "boolean"`,
		},
		{
			"nodes declared as an object",
			func(out map[string]any) {
				out["properties"].(map[string]any)["nodes"] = map[string]any{"type": "object"}
			},
			`requires type "array"`,
		},
		{
			"node items do not require an id",
			func(out map[string]any) {
				items := out["properties"].(map[string]any)["nodes"].(map[string]any)["items"].(map[string]any)
				items["required"] = []any{"type"}
			},
			"outputSchema.nodes[]",
		},
		{
			"edge items do not require the endpoints",
			func(out map[string]any) {
				items := out["properties"].(map[string]any)["edges"].(map[string]any)["items"].(map[string]any)
				items["required"] = []any{"type"}
			},
			"from_id",
		},
		{
			"nodes declares no item schema",
			func(out map[string]any) {
				delete(out["properties"].(map[string]any)["nodes"].(map[string]any), "items")
			},
			"item schema",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := contractMap(t, OutputContractJSON())
			tc.bend(out)
			err := CheckToolSchemas("collect_graph", contractMap(t, InputContractJSON()), out)
			require.Error(t, err, "a loosened schema must be refused")
			assert.Contains(t, err.Error(), tc.wantErrHas)
			assert.Contains(t, err.Error(), "collect_graph", "the refusal must name the tool")
		})
	}
}

// TestValidateResultPayload_RefusesNonConformingResults pins the payload gate,
// which is a different question from the schema gate: a provider may advertise a
// perfect schema and still return something else.
func TestValidateResultPayload_RefusesNonConformingResults(t *testing.T) {
	for _, tc := range []struct{ name, payload, wantErrHas string }{
		{"missing walk_complete", `{"nodes":[],"edges":[]}`, "walk_complete"},
		{"nodes is not an array", `{"nodes":{},"edges":[],"walk_complete":true}`, "nodes"},
		{"node missing its type", `{"nodes":[{"id":"a"}],"edges":[],"walk_complete":true}`, "type"},
		{"walk_complete is a string", `{"nodes":[],"edges":[],"walk_complete":"yes"}`, "walk_complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateResultPayload("collect_graph", []byte(tc.payload))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "collect_graph")
			assert.Contains(t, err.Error(), tc.wantErrHas)
		})
	}
}

// TestDecodeResult_RefusesAnUndefinedField pins that a typo'd key is an error
// rather than a silent drop: a dropped field is a collector author debugging an
// empty graph with no message to go on.
func TestDecodeResult_RefusesAnUndefinedField(t *testing.T) {
	_, err := DecodeResult("collect_graph", json.RawMessage(
		`{"nodes":[{"id":"a","type":"issue","summry":"typo"}],"edges":[],"walk_complete":true}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "summry")
}

// TestDecodeResult_RefusesNoStructuredContent pins the arm a provider reaches by
// answering with prose instead of a structured result.
func TestDecodeResult_RefusesNoStructuredContent(t *testing.T) {
	_, err := DecodeResult("collect_graph", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no structuredContent")
}

// contractMap decodes a contract schema into a mutable map.
func contractMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}
