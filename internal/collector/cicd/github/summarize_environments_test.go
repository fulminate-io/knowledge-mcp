// SPDX-License-Identifier: Apache-2.0

package github

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitHubEnvironment(t *testing.T) {
	got := summarizeGitHubEnvironment(cicd.ResourceSpec{
		Name: "prod", Metadata: map[string]string{"org": "o", "repo": "o/r"},
	})
	assert.Equal(t, "GitHub environment prod (o/o/r)", got)
}
