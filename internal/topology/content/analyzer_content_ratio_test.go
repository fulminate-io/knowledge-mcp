// SPDX-License-Identifier: Apache-2.0

package content

import (
	"context"
	"fmt"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContentTypeRatio verifies the aggregate ratio path: a graph with
// 30 code_blocks and 10 sections emits one Finding with code_block:0.75 and
// section:0.25. Byte-stable vs the original store-backed fixture.
func TestContentTypeRatio(t *testing.T) {
	var nodes []*knowledgev1.Node
	for i := range 30 {
		nodes = append(nodes, mkNode(fmt.Sprintf("cb-%d", i), "code_block", fmt.Sprintf("cb-%d", i)))
	}
	for i := range 10 {
		nodes = append(nodes, mkNode(fmt.Sprintf("sec-%d", i), "section", fmt.Sprintf("sec-%d", i)))
	}
	f := &fakeCaller{nodes: nodes}

	a := ContentTypeRatioAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, nil))
	require.NoError(t, err)
	require.Len(t, findings, 1, "default (per_root=false) emits one aggregate finding")

	fnd := findings[0]
	assert.Equal(t, "content-type-ratio", fnd.Algorithm)
	assert.InDelta(t, 40.0, fnd.Metrics["total_nodes"], 1e-9)
	assert.InDelta(t, 0.75, fnd.Metrics["type:code_block"], 1e-9,
		"30 code_blocks out of 40 nodes = 0.75 ratio")
	assert.InDelta(t, 0.25, fnd.Metrics["type:section"], 1e-9,
		"10 sections out of 40 nodes = 0.25 ratio")
}

// TestContentTypeRatio_PerRoot verifies the opt-in per-root path: enabling
// per_root="true" with root_types="page" emits one Finding per page in
// addition to the aggregate.
func TestContentTypeRatio_PerRoot(t *testing.T) {
	var nodes []*knowledgev1.Node
	var edges []*knowledgev1.Edge

	// Page A: 1 section + 4 paragraphs (0.8 paragraph, 0.2 section).
	nodes = append(nodes, mkNode("page-a", "page", "page-a"))
	nodes = append(nodes, mkNode("page-a-sec", "section", "page-a-sec"))
	edges = append(edges, containsEdge("page-a", "page-a-sec"))
	for i := range 4 {
		pid := fmt.Sprintf("a-p-%d", i)
		nodes = append(nodes, mkNode(pid, "paragraph", pid))
		edges = append(edges, containsEdge("page-a-sec", pid))
	}

	// Page B: 1 section + 4 code_blocks (0.8 code_block, 0.2 section).
	nodes = append(nodes, mkNode("page-b", "page", "page-b"))
	nodes = append(nodes, mkNode("page-b-sec", "section", "page-b-sec"))
	edges = append(edges, containsEdge("page-b", "page-b-sec"))
	for i := range 4 {
		cid := fmt.Sprintf("b-cb-%d", i)
		nodes = append(nodes, mkNode(cid, "code_block", cid))
		edges = append(edges, containsEdge("page-b-sec", cid))
	}

	f := &fakeCaller{nodes: nodes, edges: edges}

	a := ContentTypeRatioAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, map[string]string{"per_root": "true"}))
	require.NoError(t, err)
	require.Len(t, findings, 3, "1 aggregate + 2 per-page findings")

	// The aggregate is always the first finding.
	assert.Equal(t, "aggregate", findings[0].Metadata["scope"])
	for _, fnd := range findings[1:] {
		assert.Equal(t, "root", fnd.Metadata["scope"])
	}
}

// TestContentTypeRatio_Empty verifies the zero-node edge case: an empty graph
// produces zero findings rather than a NaN-riddled aggregate.
func TestContentTypeRatio_Empty(t *testing.T) {
	f := &fakeCaller{}

	a := ContentTypeRatioAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, nil))
	require.NoError(t, err)
	assert.Empty(t, findings)
}

// TestContentTypeRatio_Registered verifies init() self-registration.
func TestContentTypeRatio_Registered(t *testing.T) {
	a, ok := foundation.Get("content-type-ratio")
	require.True(t, ok, "content-type-ratio analyzer must be registered at package init")
	assert.Equal(t, "content-type-ratio", a.Name())
}
