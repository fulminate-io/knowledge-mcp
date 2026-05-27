// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitLabPipeline(t *testing.T) {
	assert.Equal(t, "GitLab pipeline p", summarizeGitLabPipeline(cicd.ResourceSpec{Name: "p"}))
}
