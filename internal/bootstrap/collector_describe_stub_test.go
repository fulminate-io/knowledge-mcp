// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collector_describe_stub_test.go — the DESCRIBE half of this package's stub
// provider: the conforming declaration every add-path row dials, and the knobs
// the refusal rows bend.
//
// The declaration is built from the checked-in schema's own vocabulary rather
// than from a literal an assertion also reads, so a row comparing the written
// entry to the declaration compares two things that were not typed together.

// describeStubConfig is what a case asked the stub's describe tool to be.
type describeStubConfig struct {
	// omitTool serves no describe tool at all: every provider written before it
	// existed.
	omitTool bool
	// schema replaces the advertised describe output schema.
	schema map[string]any
	// declaration replaces the returned declaration document.
	declaration map[string]any
}

type describeStubOption func(*describeStubConfig)

// withoutDescribe serves the collect tool alone.
func withoutDescribe() describeStubOption {
	return func(c *describeStubConfig) { c.omitTool = true }
}

// withDescribeSchema advertises a bent describe output schema.
func withDescribeSchema(bend func(out map[string]any)) describeStubOption {
	return func(c *describeStubConfig) {
		out := map[string]any{}
		if err := json.Unmarshal(externalcollector.DescribeContractJSON(), &out); err != nil {
			panic(err)
		}
		bend(out)
		c.schema = out
	}
}

// withDeclaration returns a declaration built by bending the conforming one.
func withDeclaration(bend func(decl map[string]any)) describeStubOption {
	return func(c *describeStubConfig) {
		decl := stubDeclarationDocument()
		bend(decl)
		c.declaration = decl
	}
}

// withBareDeclaration serves the SMALLEST conforming declaration: the three
// behavior booleans, empty vocabularies, no environment, no context, no
// overrides.
//
// It exists so a row about what the OPERATOR'S silence writes has a provider
// that contributes nothing of its own. Without it those rows could not tell "the
// add wrote nothing" from "the collector declared something that looks the
// same".
func withBareDeclaration() describeStubOption {
	return func(c *describeStubConfig) {
		c.declaration = map[string]any{
			"behavior": map[string]any{
				"summarizable": true, "embeddable": true, "syncable": true,
			},
			"node_types":  []any{},
			"edge_types":  []any{},
			"environment": []any{},
		}
	}
}

// stubDeclarationDocument is the conforming declaration this package's providers
// return: one of every half, so a row asserting the entry was filled has
// something to compare in every arm.
//
// THE ENVIRONMENT CARRIES ALL THREE CLASSES, which is what makes the three-class
// policy observable on the written entry rather than asserted about one class.
func stubDeclarationDocument() map[string]any {
	return map[string]any{
		"behavior": map[string]any{
			"summarizable":     true,
			"embeddable":       true,
			"syncable":         true,
			"embed_fields":     []any{"summary", "content"},
			"summarize_fields": []any{"content"},
			"bm25_fields":      []any{"keywords"},
		},
		"node_type_overrides": map[string]any{
			"stub-node": map[string]any{"summarizable": false, "embed_fields": []any{"summary"}},
		},
		"node_types": []any{"stub-node", "stub-other"},
		"edge_types": []any{"stub-edge"},
		"environment": []any{
			map[string]any{"name": "STUB_HOME", "class": "path"},
			map[string]any{"name": "STUB_REGION", "class": "selector"},
			map[string]any{"name": "STUB_TOKEN", "class": "secret"},
		},
		"context": map[string]any{
			"code": map[string]any{
				"node_types":  []any{"file"},
				"node_fields": []any{"id", "file_path"},
			},
		},
	}
}

// addStubDescribeTool installs the describe tool on a stub server. A nil schema
// advertises the checked-in contract file; a nil declaration returns the
// conforming document.
func addStubDescribeTool(server *mcp.Server, schema, declaration map[string]any) {
	out := schema
	if out == nil {
		out = map[string]any{}
		if err := json.Unmarshal(externalcollector.DescribeContractJSON(), &out); err != nil {
			panic(err)
		}
	}
	doc := declaration
	if doc == nil {
		doc = stubDeclarationDocument()
	}
	server.AddTool(
		&mcp.Tool{
			Name:         externalcollector.DescribeToolName,
			Description:  "stub declaration",
			InputSchema:  map[string]any{"type": "object"},
			OutputSchema: out,
		},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{StructuredContent: doc}, nil
		})
}

// declaredValue reads one path out of the stub declaration, so a row's
// expectation is taken from the document the provider served rather than from a
// second literal beside the assertion.
func declaredValue(t *testing.T, path ...string) any {
	t.Helper()
	var cur any = stubDeclarationDocument()
	for _, key := range path {
		m, ok := cur.(map[string]any)
		require.True(t, ok, "declaration path %v: %q is not an object", path, key)
		cur, ok = m[key]
		require.True(t, ok, "declaration path %v: no %q", path, key)
	}
	return cur
}

// declaredStrings reads a declared string list off the stub declaration.
func declaredStrings(t *testing.T, path ...string) []string {
	t.Helper()
	raw, ok := declaredValue(t, path...).([]any)
	require.True(t, ok, "declaration path %v is not a list", path)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		require.True(t, ok, "declaration path %v carries a non-string", path)
		out = append(out, s)
	}
	return out
}
