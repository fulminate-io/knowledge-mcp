// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestRenderResource_BothKinds drives the parametric renderer family with both
// the cloud (region) and cicd (provider) kinds against fixtures, asserting the
// node/search/browse byte-shapes match the respective server formatters
// (formatCloud* / formatCICD*), including the skip-already-shown-keys metadata
// loop.
func TestRenderResource_BothKinds(t *testing.T) {
	node := &knowledgev1.Node{
		Id:         "arn:res-1",
		SymbolName: "my-bucket",
		Summary:    "an s3 bucket",
		Keywords:   "storage",
		Metadata: map[string]string{
			"resource_type": "s3:bucket",
			"region":        "us-east-1",
			"provider":      "aws",
			"extra":         "shown",
		},
	}

	t.Run("cloud node skips resource_type+region", func(t *testing.T) {
		out := RenderResourceNode(ResourceKindCloud, "acme", node)
		text := out.Content[0].Text
		assert.Contains(t, text, "## Cloud Resource [acme]\n\n")
		assert.Contains(t, text, "**my-bucket**\n")
		assert.Contains(t, text, "Type: s3:bucket\n")
		assert.Contains(t, text, "Region: us-east-1\n")
		assert.Contains(t, text, "ID: arn:res-1\n")
		assert.Contains(t, text, "**Summary:** an s3 bucket\n")
		assert.Contains(t, text, "**Keywords:** storage\n")
		// resource_type + region skipped in the trailing loop; provider+extra shown.
		assert.Contains(t, text, "provider: aws\n")
		assert.Contains(t, text, "extra: shown\n")
		assert.NotContains(t, text, "region: us-east-1\n") // capital "Region:" only, not raw key
	})

	t.Run("cicd node skips resource_type+provider", func(t *testing.T) {
		out := RenderResourceNode(ResourceKindCICD, "acme", node)
		text := out.Content[0].Text
		assert.Contains(t, text, "## CI/CD Resource [acme]\n\n")
		assert.Contains(t, text, "Provider: aws\n")
		// resource_type + provider skipped; region+extra shown.
		assert.Contains(t, text, "region: us-east-1\n")
		assert.Contains(t, text, "extra: shown\n")
		assert.NotContains(t, text, "provider: aws\n") // capital "Provider:" only
	})

	results := []SearchResult{{Score: 0.91, Node: node}}

	t.Run("cloud search uses region in parens", func(t *testing.T) {
		out := RenderResourceSearch(ResourceKindCloud, "acme", "bucket", results, "")
		text := out.Content[0].Text
		assert.Contains(t, text, "## Cloud [acme] — 1 results for \"bucket\"\n\n")
		assert.Contains(t, text, "### 1. my-bucket [s3:bucket] (us-east-1)\n")
		assert.Contains(t, text, "0.91 — arn:res-1\n")
	})

	t.Run("cicd search uses provider in parens", func(t *testing.T) {
		out := RenderResourceSearch(ResourceKindCICD, "acme", "bucket", results, "")
		text := out.Content[0].Text
		assert.Contains(t, text, "## CI/CD [acme] — 1 results for \"bucket\"\n\n")
		assert.Contains(t, text, "### 1. my-bucket [s3:bucket] (aws)\n")
	})

	// The total argument is the CORPUS figure, so this last-page fixture passes
	// offset+len(nodes): the header states the corpus and no more-exist footer is
	// due. TestRenderResourceBrowse_PaginationFooter is where the header and the
	// footer are driven apart with a total the page length cannot coincide with.
	t.Run("cloud browse header + offset + secondary", func(t *testing.T) {
		out := RenderResourceBrowse(ResourceKindCloud, "acme", []*knowledgev1.Node{node}, 10, 11, "s3")
		text := out.Content[0].Text
		assert.Contains(t, text, "## Cloud [acme] — 11 resources (type: s3*) (offset 10)\n\n")
		assert.Contains(t, text, "11. **my-bucket** [s3:bucket] (us-east-1)\n")
		assert.Contains(t, text, "   ID: arn:res-1\n")
	})

	t.Run("cicd browse uses provider", func(t *testing.T) {
		out := RenderResourceBrowse(ResourceKindCICD, "acme", []*knowledgev1.Node{node}, 0, 1, "")
		text := out.Content[0].Text
		assert.Contains(t, text, "## CI/CD [acme] — 1 resources\n\n")
		assert.Contains(t, text, "1. **my-bucket** [s3:bucket] (aws)\n")
	})
}

// TestRenderResourceBrowse_PaginationFooter pins the two halves of the corpus
// total together: the HEADER states the corpus figure, and the more-exist footer
// appears exactly when the page is not the whole corpus.
//
// THE HEADER LEG IS WHAT STOPS A FOOTER-ONLY FIX. Before this change the header
// formatted len(nodes) — the PAGE length — so a 20-row page of a 5,000-resource
// account rendered "— 20 resources", an affirmative false statement about the
// corpus. A fix that added the footer alone would ship that header with every
// other gate green.
//
// THE FIXTURE NUMBERS ARE DELIBERATELY UNEQUAL: 3 nodes against a total of 42. A
// fixture deriving both from one value cannot tell a corpus figure from a page
// length — with 3 nodes and total 3 the old header would read correctly by
// coincidence.
func TestRenderResourceBrowse_PaginationFooter(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "arn:res-1", SymbolName: "bucket-1", Metadata: map[string]string{"resource_type": "s3:bucket"}},
		{Id: "arn:res-2", SymbolName: "bucket-2", Metadata: map[string]string{"resource_type": "s3:bucket"}},
		{Id: "arn:res-3", SymbolName: "bucket-3", Metadata: map[string]string{"resource_type": "s3:bucket"}},
	}

	t.Run("more rows exist: header carries the corpus total and the footer appears", func(t *testing.T) {
		text := RenderResourceBrowse(ResourceKindCloud, "acme", nodes, 0, 42, "").Content[0].Text
		assert.Contains(t, text, "## Cloud [acme] — 42 resources",
			"the header must state the CORPUS total, never the page length")
		assert.NotContains(t, text, "— 3 resources",
			"a page length in the header is the false statement this test exists to catch")
		assert.Contains(t, text, "_Use offset=3 to see more._",
			"3 of 42 rows shown: the reader must be told the rest exist and how to reach them")
	})

	t.Run("the page IS the corpus: no footer", func(t *testing.T) {
		text := RenderResourceBrowse(ResourceKindCloud, "acme", nodes, 0, 3, "").Content[0].Text
		assert.Contains(t, text, "## Cloud [acme] — 3 resources")
		assert.NotContains(t, text, "to see more",
			"a complete listing must not offer a next page — a footer hardcoded on would fail here")
	})

	t.Run("a later page still counts from the offset", func(t *testing.T) {
		text := RenderResourceBrowse(ResourceKindCICD, "acme", nodes, 39, 42, "").Content[0].Text
		assert.Contains(t, text, "## CI/CD [acme] — 42 resources (offset 39)")
		assert.NotContains(t, text, "to see more",
			"rows 40-42 of 42 are the last page: offset+len(nodes) == total")
	})
}
