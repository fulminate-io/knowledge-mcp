// SPDX-License-Identifier: Apache-2.0

package github

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitHubRunner(t *testing.T) {
	assert.Equal(t, "GitHub runner r", summarizeGitHubRunner(cicd.ResourceSpec{Name: "r"}))
}
