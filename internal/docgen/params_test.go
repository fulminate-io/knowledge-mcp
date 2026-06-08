// SPDX-License-Identifier: Apache-2.0

package docgen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

func sampleTool() kgtools.MCPTool {
	closed := false
	return kgtools.MCPTool{
		Name:        "demo",
		Description: "demo tool",
		InputSchema: kgtools.InputSchema{
			Type:     "object",
			Required: []string{"operation"},
			Properties: map[string]kgtools.Property{
				"operation": {
					Type:        "string",
					Description: "what to do",
					Enum:        []string{"create", "update"},
				},
				"summary": {
					Type:        "string",
					Description: "one-liner",
					MaxLength:   500,
				},
				"nested": {
					Type:                 "object",
					Description:          "an object",
					AdditionalProperties: &closed,
					Properties: map[string]kgtools.Property{
						"inner": {Type: "string", Description: "inner key"},
					},
				},
				"tags": {
					Type:        "array",
					Description: "a list",
					Items:       &kgtools.Property{Type: "string"},
				},
			},
		},
	}
}

func TestRenderParamsTable_Columns(t *testing.T) {
	out := renderParamsTable(sampleTool())

	assert.Contains(t, out, "| Parameter | Type | Required | Enum | Description |")
	// Required marker on a root-required key.
	assert.Contains(t, out, "| `operation` | string | yes | create, update | what to do |")
	// maxLength surfaced in the description cell.
	assert.Contains(t, out, "| `summary` | string |  |  | one-liner (max length: 500) |")
	// Nested object recursion with dotted path.
	assert.Contains(t, out, "| `nested` | object |")
	assert.Contains(t, out, "| `nested.inner` | string |  |  | inner key |")
	// Array element recursion with [] suffix; array type annotated with element type.
	assert.Contains(t, out, "| `tags` | array of string |")
	assert.Contains(t, out, "| `tags[]` |")
}

func TestRenderParamsTable_Deterministic(t *testing.T) {
	first := renderParamsTable(sampleTool())
	for range 50 {
		assert.Equal(t, first, renderParamsTable(sampleTool()), "render must be byte-identical across runs (sorted keys)")
	}
	// Sorted order: nested before operation before summary before tags.
	assert.Less(t, strings.Index(first, "`nested`"), strings.Index(first, "`operation`"))
	assert.Less(t, strings.Index(first, "`operation`"), strings.Index(first, "`summary`"))
	assert.Less(t, strings.Index(first, "`summary`"), strings.Index(first, "`tags`"))
}

func TestRenderParamsTable_NoParams(t *testing.T) {
	tool := kgtools.MCPTool{Name: "noargs", Description: "x", InputSchema: kgtools.InputSchema{Type: "object"}}
	out := renderParamsTable(tool)
	assert.Contains(t, out, "_(no parameters)_")
}

// TestRenderParamsTable_EveryCatalogTool renders every real tool to prove the
// renderer survives the full live schema catalog without panicking and stays
// deterministic against it.
func TestRenderParamsTable_EveryCatalogTool(t *testing.T) {
	for _, tool := range tools.AllToolSchemas() {
		out := renderParamsTable(tool)
		require.NotEmpty(t, out, "tool %q rendered empty", tool.Name)
		assert.Equal(t, out, renderParamsTable(tool), "tool %q render must be deterministic", tool.Name)
	}
}
