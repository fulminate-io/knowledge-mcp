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
		out := RenderResourceSearch(ResourceKindCloud, "acme", "bucket", results)
		text := out.Content[0].Text
		assert.Contains(t, text, "## Cloud [acme] — 1 results for \"bucket\"\n\n")
		assert.Contains(t, text, "### 1. my-bucket [s3:bucket] (us-east-1)\n")
		assert.Contains(t, text, "0.91 — arn:res-1\n")
	})

	t.Run("cicd search uses provider in parens", func(t *testing.T) {
		out := RenderResourceSearch(ResourceKindCICD, "acme", "bucket", results)
		text := out.Content[0].Text
		assert.Contains(t, text, "## CI/CD [acme] — 1 results for \"bucket\"\n\n")
		assert.Contains(t, text, "### 1. my-bucket [s3:bucket] (aws)\n")
	})

	t.Run("cloud browse header + offset + secondary", func(t *testing.T) {
		out := RenderResourceBrowse(ResourceKindCloud, "acme", []*knowledgev1.Node{node}, 10, "s3")
		text := out.Content[0].Text
		assert.Contains(t, text, "## Cloud [acme] — 1 resources (type: s3*) (offset 10)\n\n")
		assert.Contains(t, text, "11. **my-bucket** [s3:bucket] (us-east-1)\n")
		assert.Contains(t, text, "   ID: arn:res-1\n")
	})

	t.Run("cicd browse uses provider", func(t *testing.T) {
		out := RenderResourceBrowse(ResourceKindCICD, "acme", []*knowledgev1.Node{node}, 0, "")
		text := out.Content[0].Text
		assert.Contains(t, text, "## CI/CD [acme] — 1 resources\n\n")
		assert.Contains(t, text, "1. **my-bucket** [s3:bucket] (aws)\n")
	})
}
