// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stubprovider_describe_test.go — the DESCRIBE half of this package's stub
// provider: the schema each mode advertises for the required second tool, the
// declaration it returns, and the two arms that break their own word. Split out
// of stubprovider_test.go when that file reached this repository's per-file size
// budget; the modes, the re-exec harness and the collect half stay there.

// stubDescribeSchema returns the describe output schema this mode advertises.
func stubDescribeSchema(mode string) any {
	if mode == stubModeBadDescribeSchema {
		// Conforming but for the node vocabulary, which is the half the ingest
		// refusal reads: a provider that does not declare it declares nothing the
		// server can refuse against.
		out := contractSchemaMap(DescribeContractJSON())
		out["required"] = []any{"behavior", "edge_types", "environment"}
		delete(out["properties"].(map[string]any), "node_types")
		return out
	}
	return contractSchemaMap(DescribeContractJSON())
}

// stubDeclarationDocument is the conforming declaration every mode but the two
// describe-failure arms returns.
func stubDeclarationDocument() map[string]any {
	return map[string]any{
		"behavior": map[string]any{
			"summarizable": true, "embeddable": false, "syncable": true,
			"embed_fields": []any{"summary"},
		},
		"node_types": []any{"issue", "epic"},
		"edge_types": []any{"blocks"},
		"environment": []any{
			map[string]any{"name": declaredEnv, "class": "selector"},
		},
	}
}

// stubDescribeHandler answers the describe tool for one mode. The raw handler
// form is used for the same reason the collect handler uses it: the generic
// AddTool would validate the result server-side, which would make the
// "provider breaks its own word" arm untestable.
func stubDescribeHandler(mode string) mcp.ToolHandler {
	return func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		switch mode {
		case stubModeDescribeError:
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "the collector cannot describe itself right now"}},
			}, nil
		case stubModeBadDeclaration:
			doc := stubDeclarationDocument()
			delete(doc, "behavior")
			return &mcp.CallToolResult{StructuredContent: doc}, nil
		default:
			return &mcp.CallToolResult{StructuredContent: stubDeclarationDocument()}, nil
		}
	}
}
