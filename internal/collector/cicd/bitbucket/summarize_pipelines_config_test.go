// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeBBPipeline(t *testing.T) {
	got := summarizeBBPipeline(cicd.ResourceSpec{
		Name:     "r/build",
		Metadata: map[string]string{"trigger_key": "default", "workspace": "ws", "repo": "r"},
	})
	assert.Contains(t, got, "Bitbucket pipeline r/build")
	assert.Contains(t, got, "trigger=default")
	assert.Contains(t, got, "(ws/r)")
}
