// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitLabRunner(t *testing.T) {
	assert.Equal(t, "GitLab runner r", summarizeGitLabRunner(cicd.ResourceSpec{Name: "r"}))
}

func TestSummarizeGitLabRunnerTag(t *testing.T) {
	assert.Equal(t, "GitLab runner tag t", summarizeGitLabRunnerTag(cicd.ResourceSpec{Name: "t"}))
}
