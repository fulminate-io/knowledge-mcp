// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeBBEnvironment(t *testing.T) {
	assert.Equal(t, "Bitbucket environment e", summarizeBBEnvironment(cicd.ResourceSpec{Name: "e"}))
}

func TestSummarizeBBApprovalGate(t *testing.T) {
	assert.Equal(t, "Bitbucket approval gate g", summarizeBBApprovalGate(cicd.ResourceSpec{Name: "g"}))
}
