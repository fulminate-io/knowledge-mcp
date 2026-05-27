// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeBBPipelineRun(t *testing.T) {
	got := summarizeBBPipelineRun(cicd.ResourceSpec{
		Name:     "ws/r #1",
		Metadata: map[string]string{"state": "COMPLETED", "result": "SUCCESSFUL", "workspace": "ws"},
	})
	assert.Contains(t, got, "Bitbucket pipeline run ws/r #1")
	assert.Contains(t, got, "state=COMPLETED")
	assert.Contains(t, got, "result=SUCCESSFUL")
	assert.Contains(t, got, "(ws)")
}
