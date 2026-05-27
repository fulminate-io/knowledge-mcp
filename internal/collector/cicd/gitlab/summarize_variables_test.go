// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitLabVariable(t *testing.T) {
	got := summarizeGitLabVariable(cicd.ResourceSpec{
		Name:     "TOKEN",
		Metadata: map[string]string{"scope": "project", "protected": "true", "masked": "true", "project": "g/p"},
	})
	assert.Contains(t, got, "GitLab variable TOKEN")
	assert.Contains(t, got, "scope=project")
	assert.Contains(t, got, "protected")
	assert.Contains(t, got, "masked")
	assert.Contains(t, got, "(g/p)")
}
