// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func TestStorageSearchTextRetainsReference(t *testing.T) {
	id := `kgref:2:{"storage":"local","graph":"knowledge","id":"same"}`
	result := RenderForCaller("needle", []SearchResult{{Node: &knowledgev1.Node{Id: id, SymbolName: "named finding", Summary: "needle"}}}, "text", nil, "BM25-only")
	require.Contains(t, FirstTextContent(result), id)
}
