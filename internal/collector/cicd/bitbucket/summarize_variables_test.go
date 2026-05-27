// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeBBVariable(t *testing.T) {
	assert.Equal(t, "Bitbucket variable v", summarizeBBVariable(cicd.ResourceSpec{Name: "v"}))
}
