// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// bornLinkEdges returns the relates-to edges carrying Method="code-ref" across
// every recorded create Mutation — the born-link edges minted by the arm.
func bornLinkEdges(fc *fakeGraphCaller) []*knowledgev1.BatchEdgeSpec {
	var out []*knowledgev1.BatchEdgeSpec
	for _, m := range fc.execMutations {
		if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			continue
		}
		for _, e := range m.GetEdges() {
			if e.GetType() == string(kgtypes.EdgeRelatesTo) && e.GetMethod() == codeRefMethod {
				out = append(out, e)
			}
		}
	}
	return out
}

// TestComposeThoughtCreate_BornLink covers the born-link arm end-to-end through
// composeThoughtCreate → PersistBatch → engine.Compile: a resolvable referent
// produces ONE create whose create_batch plan carries a thought relates-to proxy
// BatchEdgeSpec with GetMethod()=code-ref; a referent-free think writes zero
// born-link edges; an unresolvable referent never blocks the think and writes
// zero born-link edges.
func TestComposeThoughtCreate_BornLink(t *testing.T) {
	const repo = "knowledge"
	const ref = "tools/wire.go:PersistBatch"
	wantProxyID := "proxy:" + repo + ":" + ref

	t.Run("resolvable referent born-links with Method code-ref on the create_batch", func(t *testing.T) {
		fc := &fakeGraphCaller{
			listGraphsResult: listGraphsResultJSON(t, [2]string{"code", repo}),
			queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
				{Type: "code", Name: repo}: {
					ref: nodeResultJSON(t, ref, "function_declaration", nil),
				},
			},
			mutateIDs: []string{"th-new"},
		}
		receipt, err := composeThoughtCreate(context.Background(), fc, composeThoughtArgs{
			Content: "the bug is in " + ref + " today",
			Summary: "born-link smoke",
		})
		require.NoError(t, err)
		assert.Equal(t, "th-new", receipt.ID)
		assert.Equal(t, 1, receipt.BornLinks, "the receipt counts the born-link edge it minted")

		// Exactly one create Mutation, and it carries the born-link edge.
		var creates int
		for _, m := range fc.execMutations {
			if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
				creates++
			}
		}
		require.Equal(t, 1, creates, "the thought rides exactly ONE create_batch")

		bl := bornLinkEdges(fc)
		require.Len(t, bl, 1, "exactly one born-link relates-to code-ref edge")
		assert.Equal(t, wantProxyID, bl[0].GetToId(), "edge targets the deterministic code proxy")
		assert.Equal(t, int32(0), bl[0].GetFromIdx(), "edge originates at the thought slot (0)")
		assert.Equal(t, codeRefMethod, bl[0].GetMethod(), "Method=code-ref survives to the decoded edge")
	})

	t.Run("no referents → zero born-link edges, identical to today", func(t *testing.T) {
		fc := &fakeGraphCaller{
			listGraphsResult: listGraphsResultJSON(t, [2]string{"code", repo}),
			mutateIDs:        []string{"th-plain"},
		}
		receipt, err := composeThoughtCreate(context.Background(), fc, composeThoughtArgs{
			Content: "a prose-only thought with no code citation whatsoever",
			Summary: "no referent",
		})
		require.NoError(t, err)
		assert.Equal(t, "th-plain", receipt.ID)
		assert.Zero(t, receipt.BornLinks, "no referents → the receipt reports zero born-links")
		assert.Empty(t, bornLinkEdges(fc), "a referent-free think writes zero born-link edges")
	})

	t.Run("only unresolvable referents → thought created, zero born-link edges", func(t *testing.T) {
		fc := &fakeGraphCaller{
			listGraphsResult: listGraphsResultJSON(t, [2]string{"code", repo}),
			// code graph configured but the cited referent is absent from it.
			queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
				{Type: "code", Name: repo}: {},
			},
			mutateIDs: []string{"th-unresolv"},
		}
		receipt, err := composeThoughtCreate(context.Background(), fc, composeThoughtArgs{
			Content: "cites tools/ghost.go:Missing which resolves nowhere",
			Summary: "unresolvable referent",
		})
		require.NoError(t, err, "an unresolvable referent must never block the think")
		assert.Equal(t, "th-unresolv", receipt.ID)
		assert.Zero(t, receipt.BornLinks, "an unresolvable referent contributes no born-link to the receipt")
		assert.Empty(t, bornLinkEdges(fc), "an unresolvable referent writes zero born-link edges")
	})
}
