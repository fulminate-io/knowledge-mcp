// SPDX-License-Identifier: Apache-2.0

package github

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitHubOrganization(t *testing.T) {
	assert.Equal(t, "GitHub organization myorg", summarizeGitHubOrganization(cicd.ResourceSpec{Name: "myorg"}))
}

func TestSummarizeGitHubRepository(t *testing.T) {
	got := summarizeGitHubRepository(cicd.ResourceSpec{
		Name: "o/r", Metadata: map[string]string{"visibility": "public", "default_branch": "main"},
	})
	assert.Contains(t, got, "GitHub repository o/r")
	assert.Contains(t, got, "visibility=public")
	assert.Contains(t, got, "default=main")
}
