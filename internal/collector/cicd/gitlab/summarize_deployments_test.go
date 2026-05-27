// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitLabDeployment(t *testing.T) {
	assert.Equal(t, "GitLab deployment d", summarizeGitLabDeployment(cicd.ResourceSpec{Name: "d"}))
}
