// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitLabEnvironment(t *testing.T) {
	assert.Equal(t, "GitLab environment e", summarizeGitLabEnvironment(cicd.ResourceSpec{Name: "e"}))
}

func TestSummarizeGitLabProtectionRule(t *testing.T) {
	got := summarizeGitLabProtectionRule(cicd.ResourceSpec{
		Name: "prod-protect",
		Metadata: map[string]string{
			"environment": "prod", "project": "g/p", "required_approval_count": "2",
		},
	})
	assert.Contains(t, got, "GitLab protection rule prod-protect")
	assert.Contains(t, got, "approvals=2")
	assert.Contains(t, got, "env=prod")
	assert.Contains(t, got, "(g/p)")
}
