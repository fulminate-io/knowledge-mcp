// SPDX-License-Identifier: Apache-2.0

package github

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitHubWorkflow(t *testing.T) {
	got := summarizeGitHubWorkflow(cicd.ResourceSpec{
		Name: "CI", Metadata: map[string]string{"path": ".github/workflows/ci.yml", "repo": "o/r"},
	})
	assert.Contains(t, got, "GitHub workflow CI")
	assert.Contains(t, got, "path=.github/workflows/ci.yml")
	assert.Contains(t, got, "(o/r)")
}
