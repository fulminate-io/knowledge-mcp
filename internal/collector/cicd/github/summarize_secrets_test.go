// SPDX-License-Identifier: Apache-2.0

package github

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitHubSecret(t *testing.T) {
	got := summarizeGitHubSecret(cicd.ResourceSpec{
		Name: "TOKEN", Metadata: map[string]string{"scope": "org", "org": "o"},
	})
	assert.Contains(t, got, "GitHub secret TOKEN")
	assert.Contains(t, got, "scope=org")
	assert.Contains(t, got, "(o)")
}
