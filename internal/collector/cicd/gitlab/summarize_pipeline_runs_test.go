// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitLabPipelineRun(t *testing.T) {
	got := summarizeGitLabPipelineRun(cicd.ResourceSpec{
		Name:     "pipeline #1",
		Metadata: map[string]string{"status": "success", "ref": "main", "project": "g/p"},
	})
	assert.Contains(t, got, "GitLab pipeline run pipeline #1")
	assert.Contains(t, got, "status=success")
	assert.Contains(t, got, "ref=main")
	assert.Contains(t, got, "(g/p)")
}

func TestSummarizeGitLabJob(t *testing.T) {
	assert.Equal(t, "GitLab job j", summarizeGitLabJob(cicd.ResourceSpec{Name: "j"}))
}
