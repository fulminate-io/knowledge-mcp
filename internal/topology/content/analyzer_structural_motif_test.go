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

// buildMotifFixture seeds a fakeCaller corpus mirroring the original store
// fixture:
//   - 5 sections each with shape (section → 2 paragraphs). These 5 sections
//     group into one motif.
//   - 3 sections each with shape (section → 1 paragraph + 1 code_block). These
//     3 sections share a second motif (member count 3 = default min_members).
//
// Pages contain the sections (page → section EdgeContains) but root_types is
// "section" in the tests, so only the sections participate as motif roots.
func buildMotifFixture() *fakeCaller {
	var nodes []*knowledgev1.Node
	var edges []*knowledgev1.Edge

	// 5 sections with shape section → paragraph + paragraph.
	for i := range 5 {
		pageID := fmt.Sprintf("a-page-%d", i)
		secID := fmt.Sprintf("a-sec-%d", i)
		nodes = append(nodes, mkNode(pageID, "page", fmt.Sprintf("shape-a-page-%d", i)))
		nodes = append(nodes, mkNode(secID, "section", fmt.Sprintf("shape-a-sec-%d", i)))
		edges = append(edges, containsEdge(pageID, secID))
		for j := range 2 {
			pID := fmt.Sprintf("a-p-%d-%d", i, j)
			nodes = append(nodes, mkNode(pID, "paragraph", fmt.Sprintf("shape-a-p-%d-%d", i, j)))
			edges = append(edges, containsEdge(secID, pID))
		}
	}

	// 3 sections with shape section → paragraph + code_block.
	for i := range 3 {
		pageID := fmt.Sprintf("b-page-%d", i)
		secID := fmt.Sprintf("b-sec-%d", i)
		nodes = append(nodes, mkNode(pageID, "page", fmt.Sprintf("shape-b-page-%d", i)))
		nodes = append(nodes, mkNode(secID, "section", fmt.Sprintf("shape-b-sec-%d", i)))
		edges = append(edges, containsEdge(pageID, secID))
		pID := fmt.Sprintf("b-p-%d", i)
		nodes = append(nodes, mkNode(pID, "paragraph", fmt.Sprintf("shape-b-p-%d", i)))
		edges = append(edges, containsEdge(secID, pID))
		cbID := fmt.Sprintf("b-cb-%d", i)
		nodes = append(nodes, mkNode(cbID, "code_block", fmt.Sprintf("shape-b-cb-%d", i)))
		edges = append(edges, containsEdge(secID, cbID))
	}

	return &fakeCaller{nodes: nodes, edges: edges}
}

// TestStructuralMotif verifies the default-parameter happy path: the 5-section
// motif surfaces as one Finding; the 3-section motif matches the default
// min_members=3 threshold and also surfaces. Two motifs, ranked by size.
func TestStructuralMotif(t *testing.T) {
	f := buildMotifFixture()

	a := StructuralMotifAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, map[string]string{"root_types": "section"}))
	require.NoError(t, err)
	require.Len(t, findings, 2, "fixture has two distinct section-subtree motifs")

	// Findings ranked by member_count descending — 5-member motif first.
	assert.InDelta(t, 5.0, findings[0].Metrics["member_count"], 1e-9)
	assert.InDelta(t, 3.0, findings[1].Metrics["member_count"], 1e-9)

	// Every finding carries the skeleton hash in Metadata and uses the
	// "structural-motif" algorithm name.
	for _, fnd := range findings {
		assert.Equal(t, "structural-motif", fnd.Algorithm)
		assert.NotEmpty(t, fnd.Metadata["skeleton_hash"])
		assert.NotEmpty(t, fnd.Evidence, "motif finding must carry member IDs as evidence")
	}
}

// TestStructuralMotif_MinMembers verifies the min_members knob: raising it to 4
// hides the 3-section motif while keeping the 5-section motif.
func TestStructuralMotif_MinMembers(t *testing.T) {
	f := buildMotifFixture()

	a := StructuralMotifAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, map[string]string{
		"root_types":  "section",
		"min_members": "4",
	}))
	require.NoError(t, err)
	require.Len(t, findings, 1, "min_members=4 should only surface the 5-node motif")
	assert.InDelta(t, 5.0, findings[0].Metrics["member_count"], 1e-9)
}

// TestStructuralMotif_Registered verifies init() self-registration.
func TestStructuralMotif_Registered(t *testing.T) {
	a, ok := foundation.Get("structural-motif")
	require.True(t, ok, "structural-motif analyzer must be registered at package init")
	assert.Equal(t, "structural-motif", a.Name())
}
