// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestBm25FieldsFromProto asserts the wire→Document.Fields mapping: documented
// keys, empty-value skipping, and nil/empty-message defensiveness (client
// criterion).
func TestBm25FieldsFromProto(t *testing.T) {
	// nil message → nil map (the engine drops a zero-field Document defensively).
	require.Nil(t, bm25FieldsFromProto(nil))

	// All-empty message → nil map (no empty Document built).
	require.Nil(t, bm25FieldsFromProto(&knowledgev1.Bm25Fields{}))

	// Full message → all five documented keys.
	full := &knowledgev1.Bm25Fields{
		SymbolName:  "syncClusters",
		Summary:     "a summary",
		Keywords:    "kw1 kw2",
		Description: "a description",
		Content:     "body content",
	}
	require.Equal(t, map[string]string{
		searchengine.FieldSymbolName:  "syncClusters",
		searchengine.FieldSummary:     "a summary",
		searchengine.FieldKeywords:    "kw1 kw2",
		searchengine.FieldDescription: "a description",
		searchengine.FieldContent:     "body content",
	}, bm25FieldsFromProto(full))

	// Partial message → only the non-empty keys (empty values skipped).
	partial := &knowledgev1.Bm25Fields{SymbolName: "alpha", Keywords: "kw"}
	require.Equal(t, map[string]string{
		searchengine.FieldSymbolName: "alpha",
		searchengine.FieldKeywords:   "kw",
	}, bm25FieldsFromProto(partial))
}
