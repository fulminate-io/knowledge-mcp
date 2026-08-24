// SPDX-License-Identifier: Apache-2.0

package parser_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
)

// TestToBatchEdges_AlwaysIDAddressed pins the contract the incremental diff
// rests on: every edge this producer emits is ID-ADDRESSED — FromIdx and ToIdx
// are -1, and both FromID and ToID are set.
//
// WHY IT IS A CONTRACT AND NOT A DETAIL. The armed diff's filterToChangedFiles
// places an edge by resolving its FROM NODE BY ID. An index-addressed edge
// carries no FromID, so it cannot be placed; that filter now refuses one loudly
// rather than dropping it, and this test is the other half of the guard — it
// fails at the PRODUCER the moment a future path emits an index-addressed edge,
// instead of waiting for a collect to refuse in the field.
//
// The prefix argument is exercised too, because namespacing is where an ID could
// plausibly be lost: PopulateForExternalGraph routes every edge through here with
// a repo prefix, and an implementation that prefixed only one endpoint would
// still satisfy a both-non-empty check on the other.
func TestToBatchEdges_AlwaysIDAddressed(t *testing.T) {
	in := []*knowledgev1.Edge{
		{FromId: "pkg/a.go:Alpha", ToId: "pkg/b.go:Beta", Type: "CALLS", Weight: 1},
		{FromId: "pkg/b.go:Beta", ToId: "lang:repo:go", Type: "LANGUAGE"},
	}

	for _, prefix := range []string{"", "owner/repo@main/"} {
		out := parser.ToBatchEdges(in, prefix)
		// KNOWN POSITIVE: the loop below is vacuous over an empty slice, so the
		// cardinality is asserted from the fixture rather than from out itself.
		require.Len(t, out, len(in), "every input edge must be converted, prefix=%q", prefix)

		for i, e := range out {
			require.Equal(t, -1, e.FromIdx,
				"edge %d must be ID-addressed, not index-addressed (prefix=%q)", i, prefix)
			require.Equal(t, -1, e.ToIdx,
				"edge %d must be ID-addressed, not index-addressed (prefix=%q)", i, prefix)
			require.NotEmpty(t, e.FromID,
				"edge %d carries no FromID — the diff filter resolves an edge's owning file through it (prefix=%q)", i, prefix)
			require.NotEmpty(t, e.ToID, "edge %d carries no ToID (prefix=%q)", i, prefix)
			require.Equal(t, prefix+in[i].GetFromId(), e.FromID, "edge %d FromID must carry the namespace prefix", i)
			require.Equal(t, prefix+in[i].GetToId(), e.ToID, "edge %d ToID must carry the namespace prefix", i)
		}
	}
}
