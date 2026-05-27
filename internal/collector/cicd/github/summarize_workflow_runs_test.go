// SPDX-License-Identifier: Apache-2.0

package github

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitHubWorkflowRun(t *testing.T) {
	got := summarizeGitHubWorkflowRun(cicd.ResourceSpec{
		Name: "CI #1", Metadata: map[string]string{
			"status": "completed", "conclusion": "success", "event": "push", "repo": "o/r",
		},
	})
	assert.Contains(t, got, "GitHub workflow run CI #1")
	assert.Contains(t, got, "status=completed")
	assert.Contains(t, got, "conclusion=success")
	assert.Contains(t, got, "event=push")
	assert.Contains(t, got, "(o/r)")
}
