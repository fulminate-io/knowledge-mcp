// SPDX-License-Identifier: Apache-2.0

package github

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitHubDeployment(t *testing.T) {
	got := summarizeGitHubDeployment(cicd.ResourceSpec{
		Name:     "deploy/o/r/prod",
		Metadata: map[string]string{"environment": "prod", "ref": "main", "repo": "o/r"},
	})
	assert.Contains(t, got, "GitHub deployment deploy/o/r/prod")
	assert.Contains(t, got, "env=prod")
	assert.Contains(t, got, "ref=main")
	assert.Contains(t, got, "(o/r)")
}

func TestSummarizeGitHubDeployment_EmptyMeta(t *testing.T) {
	assert.Equal(t, "GitHub deployment d", summarizeGitHubDeployment(cicd.ResourceSpec{Name: "d"}))
}
