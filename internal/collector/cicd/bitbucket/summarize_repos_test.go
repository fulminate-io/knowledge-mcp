// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeBBWorkspace(t *testing.T) {
	assert.Equal(t, "Bitbucket workspace ws", summarizeBBWorkspace(cicd.ResourceSpec{Name: "ws"}))
}

func TestSummarizeBBRepository(t *testing.T) {
	assert.Equal(t, "Bitbucket repository ws/r", summarizeBBRepository(cicd.ResourceSpec{Name: "ws/r"}))
}
