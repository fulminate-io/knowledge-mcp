// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeBBRunner(t *testing.T) {
	assert.Equal(t, "Bitbucket runner r", summarizeBBRunner(cicd.ResourceSpec{Name: "r"}))
}

func TestSummarizeBBLabel(t *testing.T) {
	assert.Equal(t, "Bitbucket runner label l", summarizeBBLabel(cicd.ResourceSpec{Name: "l"}))
}
