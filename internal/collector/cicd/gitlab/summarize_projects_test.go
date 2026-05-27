// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitLabGroup(t *testing.T) {
	assert.Equal(t, "GitLab group g", summarizeGitLabGroup(cicd.ResourceSpec{Name: "g"}))
}

func TestSummarizeGitLabProject(t *testing.T) {
	assert.Equal(t, "GitLab project p", summarizeGitLabProject(cicd.ResourceSpec{Name: "p"}))
}
