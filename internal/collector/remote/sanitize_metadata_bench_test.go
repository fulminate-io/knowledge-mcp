// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// BenchmarkSanitizeNodeText_CleanMetadata measures the path every real collect
// takes: a node whose metadata is already clean. The clean case is the one that
// matters, because it is what runs on every node of every collect, including
// code collects of hundreds of thousands of nodes.
//
// The server carries a TWIN of this name in its store package. They are
// different packages and different bodies, and each measurement is extracted
// from its own invocation.
func BenchmarkSanitizeNodeText_CleanMetadata(b *testing.B) {
	n := &knowledgev1.Node{
		Id:          "bench-node",
		Type:        "function",
		SymbolName:  "DoTheThing",
		FilePath:    "cmd/knowledge/internal/collector/remote/sanitize.go",
		Language:    "go",
		Description: "a perfectly ordinary description carrying no invalid bytes",
		Metadata: map[string]string{
			"package":    "remote",
			"receiver":   "",
			"visibility": "unexported",
			"start_line": "112",
			"end_line":   "126",
			"kind":       "func",
			"module":     "knowledge",
			"generated":  "false",
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sanitizeNodeText(n)
	}
}
