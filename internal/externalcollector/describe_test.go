// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// describe_test.go — the DESCRIBE half of the contract: the checked-in schema,
// the comparator arms against it, and the declaration decode.
//
// The comparator rows are derived from the collect tool's own pair
// ("accepts the contract verbatim and stricter" / "refuses each loosening
// individually") rather than hand-enumerated, because that pair is the
// behavior this side must match exactly.

// TestDescribeContract_RequiresTheDeclarationsFourHalves pins the checked-in
// artifact a collector author copies, rather than a transcription of it.
func TestDescribeContract_RequiresTheDeclarationsFourHalves(t *testing.T) {
	var schema map[string]any
	require.NoError(t, json.Unmarshal(DescribeContractJSON(), &schema))

	assert.Equal(t, "object", schema["type"])
	assert.ElementsMatch(t, []any{"behavior", "node_types", "edge_types", "environment"}, schema["required"],
		"a declaration that omits any of the four says nothing about that half, which is not distinguishable from declaring nothing")

	props := schema["properties"].(map[string]any)
	assert.Equal(t, "array", props["node_types"].(map[string]any)["type"])
	assert.Equal(t, "array", props["edge_types"].(map[string]any)["type"])
	assert.Equal(t, "array", props["environment"].(map[string]any)["type"])
	assert.Equal(t, "object", props["behavior"].(map[string]any)["type"])

	env := props["environment"].(map[string]any)["items"].(map[string]any)
	assert.ElementsMatch(t, []any{"name", "class"}, env["required"],
		"an environment row without a class cannot be written by an installer")
	assert.ElementsMatch(t, []any{"path", "selector", "secret", "not-carried"},
		env["properties"].(map[string]any)["class"].(map[string]any)["enum"],
		"the disposition vocabulary is CLOSED at four; a fifth would change what reaches a collector on every collect, "+
			"and the fourth is what lets a collector declare a name it reads that no installed entry carries")
	assert.NotContains(t, env["properties"], "value",
		"the declaration is of NAMES; a value on it would put a credential in the describe result")
}

// TestCheckDescribeToolSchema_MissingSchema pins the absence arm: a provider
// serving describe with no output schema declares nothing at all.
func TestCheckDescribeToolSchema_MissingSchema(t *testing.T) {
	err := CheckDescribeToolSchema("describe", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NO output schema")
	assert.Contains(t, err.Error(), "describe")
	assert.Contains(t, err.Error(), "collector_describe.schema.json",
		"the refusal points at the checked-in file, so an author knows what to copy")
}

// TestCheckDescribeToolSchema_AcceptsTheContractVerbatimAndStricter pins what
// the comparator ADMITS on this side: verbatim, and stricter.
func TestCheckDescribeToolSchema_AcceptsTheContractVerbatimAndStricter(t *testing.T) {
	require.NoError(t, CheckDescribeToolSchema("describe", contractMap(t, DescribeContractJSON())))

	stricter := contractMap(t, DescribeContractJSON())
	stricter["required"] = []any{"behavior", "node_types", "edge_types", "environment", "collector_name"}
	stricter["properties"].(map[string]any)["collector_name"] = map[string]any{"type": "string"}
	require.NoError(t, CheckDescribeToolSchema("describe", stricter),
		"a provider declaring MORE than the contract is conforming; the contract is a floor, not an equality")
}

// TestCheckDescribeToolSchema_RefusesEachLooseningIndividually walks the ways
// an advertised describe schema can fall short. Each row bends ONE thing.
func TestCheckDescribeToolSchema_RefusesEachLooseningIndividually(t *testing.T) {
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
			"node_types not required",
			func(out map[string]any) { out["required"] = []any{"behavior", "edge_types", "environment"} },
			"node_types",
		},
		{
			"edge_types property missing",
			func(out map[string]any) { delete(out["properties"].(map[string]any), "edge_types") },
			"edge_types",
		},
		{
			"node_types declared as a string",
			func(out map[string]any) {
				out["properties"].(map[string]any)["node_types"] = map[string]any{"type": "string"}
			},
			`requires type "array"`,
		},
		{
			"node_types declares no item schema",
			func(out map[string]any) {
				delete(out["properties"].(map[string]any)["node_types"].(map[string]any), "items")
			},
			"item schema",
		},
		{
			"environment items do not require the class",
			func(out map[string]any) {
				items := out["properties"].(map[string]any)["environment"].(map[string]any)["items"].(map[string]any)
				items["required"] = []any{"name"}
			},
			"class",
		},
		{
			"behavior does not require the three booleans",
			func(out map[string]any) {
				out["properties"].(map[string]any)["behavior"].(map[string]any)["required"] = []any{"syncable"}
			},
			"summarizable",
		},
		{
			"behavior declared as an array",
			func(out map[string]any) {
				out["properties"].(map[string]any)["behavior"] = map[string]any{"type": "array"}
			},
			`requires type "object"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := contractMap(t, DescribeContractJSON())
			tc.bend(out)
			err := CheckDescribeToolSchema("describe", out)
			require.Error(t, err, "a loosened describe schema must be refused")
			assert.Contains(t, err.Error(), tc.wantErrHas)
			assert.Contains(t, err.Error(), "describe", "the refusal must name the tool")
		})
	}
}

// TestDecodeDeclaration_ValidatesAtCallTime pins the CALL-time gate, which is a
// different question from the listing-time one: a provider may advertise a
// perfect schema and return something else.
func TestDecodeDeclaration_ValidatesAtCallTime(t *testing.T) {
	for _, tc := range []struct{ name, payload, wantErrHas string }{
		{"no behavior", `{"node_types":["a"],"edge_types":[],"environment":[]}`, "behavior"},
		{"no node_types", `{"behavior":{"summarizable":false,"embeddable":false,"syncable":true},"edge_types":[],"environment":[]}`, "node_types"},
		{"node_types is not an array", `{"behavior":{"summarizable":false,"embeddable":false,"syncable":true},"node_types":"a","edge_types":[],"environment":[]}`, "node_types"},
		{"an environment row with no class", `{"behavior":{"summarizable":false,"embeddable":false,"syncable":true},"node_types":[],"edge_types":[],"environment":[{"name":"HOME"}]}`, "class"},
		{"an unknown environment class", `{"behavior":{"summarizable":false,"embeddable":false,"syncable":true},"node_types":[],"edge_types":[],"environment":[{"name":"HOME","class":"token"}]}`, "environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeDeclaration("describe", json.RawMessage(tc.payload))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "describe")
			assert.Contains(t, err.Error(), tc.wantErrHas)
		})
	}
}

// TestDecodeDeclaration_RefusesAnUndefinedField pins the strict decode. The
// row that matters is `value` on an environment entry: a declaration is of
// NAMES, and a provider sending a value must be refused rather than have the
// value silently dropped into a field nothing reads.
func TestDecodeDeclaration_RefusesAnUndefinedField(t *testing.T) {
	_, err := DecodeDeclaration("describe", json.RawMessage(
		`{"behavior":{"summarizable":false,"embeddable":false,"syncable":true},"node_types":[],"edge_types":[],`+
			`"environment":[{"name":"AWS_REGION","class":"selector","value":"us-east-1"}]}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "value")

	_, err = DecodeDeclaration("describe", json.RawMessage(
		`{"behavior":{"summarizable":false,"embeddable":false,"syncable":true},"node_types":[],"edge_types":[],`+
			`"environment":[],"contxt":{}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contxt")
}

// TestDecodeDeclaration_RefusesAContextCarryingReason pins the one key ticket
// 39 established is refused by the entry's own decoder: a rendered `reason`
// would make the written entry unloadable, so it is refused here, at the seam
// that can still name it.
func TestDecodeDeclaration_RefusesAContextCarryingReason(t *testing.T) {
	_, err := DecodeDeclaration("describe", json.RawMessage(
		`{"behavior":{"summarizable":false,"embeddable":false,"syncable":true},"node_types":[],"edge_types":[],`+
			`"environment":[],"context":{"aws":{"node_types":["aws:ec2:instance"],"reason":"correlation"}}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reason")
}

// TestDecodeDeclaration_AcceptsTheWholeDocument pins the positive: every half
// of a full declaration reaches the decoded value.
func TestDecodeDeclaration_AcceptsTheWholeDocument(t *testing.T) {
	decl, err := DecodeDeclaration("describe", json.RawMessage(`{
	  "behavior":{"summarizable":true,"embeddable":true,"syncable":true,
	              "embed_fields":["summary"],"summarize_fields":["content"],"bm25_fields":["keywords"]},
	  "node_type_overrides":{"log-template":{"summarizable":false,"embed_fields":["content"]}},
	  "node_types":["log-template","log-stream"],
	  "edge_types":["emits"],
	  "environment":[{"name":"HOME","class":"path"},{"name":"LOKI_URL","class":"selector","description":"the endpoint"}],
	  "context":{"aws":{"node_types":["aws:ec2:instance"],"node_fields":["id"],"metadata_keys":["region"],"edge_fields":["from_id","to_id"]}}
	}`))
	require.NoError(t, err)
	require.NotNil(t, decl.Behavior)
	assert.True(t, *decl.Behavior.Summarizable)
	assert.Equal(t, []string{"summary"}, decl.Behavior.EmbedFields)
	assert.Equal(t, []string{"log-template", "log-stream"}, decl.NodeTypes)
	assert.Equal(t, []string{"emits"}, decl.EdgeTypes)
	require.Len(t, decl.Environment, 2)
	assert.Equal(t, "HOME", decl.Environment[0].Name)
	assert.Equal(t, EnvClassPath, decl.Environment[0].Class)
	assert.Equal(t, EnvClassSelector, decl.Environment[1].Class)
	require.Contains(t, decl.NodeTypeOverrides, "log-template")
	assert.False(t, *decl.NodeTypeOverrides["log-template"].Summarizable)
	require.Contains(t, decl.Context, "aws")
	assert.Equal(t, []string{"aws:ec2:instance"}, decl.Context["aws"].NodeTypes)
}

// TestDeclaration_RefusesAnEnvironmentNameThatIsNotAVariableName pins the one
// value-shaped refusal an installer depends on: install.sh refuses a row whose
// name carries a character outside [A-Za-z0-9_], and a declaration that could
// produce one would fail the install rather than the add.
func TestDeclaration_RefusesAnEnvironmentNameThatIsNotAVariableName(t *testing.T) {
	decl, err := DecodeDeclaration("describe", json.RawMessage(
		`{"behavior":{"summarizable":false,"embeddable":false,"syncable":true},"node_types":[],"edge_types":[],`+
			`"environment":[{"name":"AWS REGION","class":"selector"}]}`))
	require.Error(t, err)
	assert.Nil(t, decl)
	assert.Contains(t, err.Error(), "AWS REGION")
}

// TestRunMCP_RefusesAProviderThatStoppedServingDescribe is the COLLECT-time half
// of the requirement, and it is a row of its own because nothing else observes
// it: `collector add` refuses a describe-less provider by CALLING describe, so a
// gate that only ran at add would leave every later dial unchecked.
//
// TWO WAYS THAT MATTERS. A provider downgraded in place after an operator
// installed it must be refused the next time it is dialed — the requirement is a
// property of the provider rather than of the entry. And a HAND-EDITED entry
// passes through no write path at all, so verifying only at add would make hand
// editing a way past a required tool.
func TestRunMCP_RefusesAProviderThatStoppedServingDescribe(t *testing.T) {
	_, _, err := RunMCP(context.Background(), stdioDef(t, stubModeNoDescribe), nil, "board", nil)
	require.Error(t, err, "a collect against a provider that does not serve describe must be refused")
	assert.Contains(t, err.Error(), DescribeToolName)
	assert.Contains(t, err.Error(), "REQUIRED")

	// THE SAME-RUN CONTROL: the identical dial against a conforming provider
	// collects. Without it this row could not tell the describe refusal from a
	// harness that refuses everything.
	res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeConforming), nil, "board", nil)
	require.NoError(t, err)
	require.NotNil(t, res)
}

// TestRunMCP_RefusesADescribeToolWhoseSchemaFallsShort pins the collect-time
// SCHEMA arm on the same terms: a provider serving describe with a schema that
// declares no node vocabulary is refused before the collect tool is called.
func TestRunMCP_RefusesADescribeToolWhoseSchemaFallsShort(t *testing.T) {
	_, _, err := RunMCP(context.Background(), stdioDef(t, stubModeBadDescribeSchema), nil, "board", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node_types")
}

// TestVerifyRegistration_RefusesTheTwoDescribeCallFailures covers the arms only
// the CALL can reach: a provider whose describe tool reports an error result,
// and one whose declaration violates the schema it advertised.
func TestVerifyRegistration_RefusesTheTwoDescribeCallFailures(t *testing.T) {
	for _, tc := range []struct{ mode, wantErrHas string }{
		{stubModeDescribeError, "cannot describe itself"},
		{stubModeBadDeclaration, "behavior"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			decl, err := VerifyRegistration(context.Background(), stdioDef(t, tc.mode))
			require.Error(t, err)
			assert.Nil(t, decl, "a refused declaration returns nothing for a caller to write")
			assert.Contains(t, err.Error(), tc.wantErrHas)
		})
	}
}

// TestVerifyRegistration_ReturnsTheProvidersDeclaration is the positive: one
// dial verifies AND returns what the entry is filled from.
func TestVerifyRegistration_ReturnsTheProvidersDeclaration(t *testing.T) {
	decl, err := VerifyRegistration(context.Background(), stdioDef(t, stubModeConforming))
	require.NoError(t, err)
	require.NotNil(t, decl)
	assert.Equal(t, []string{"issue", "epic"}, decl.NodeTypes)
	assert.Equal(t, []string{"blocks"}, decl.EdgeTypes)
	require.NotNil(t, decl.Behavior)
	require.NotNil(t, decl.Behavior.Summarizable)
	assert.True(t, *decl.Behavior.Summarizable, "the suggestion crosses the wire; what the entry does with it is the add's decision")
	require.Len(t, decl.Environment, 1)
	assert.Equal(t, EnvClassSelector, decl.Environment[0].Class)
}
