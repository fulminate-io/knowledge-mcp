// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeWorkflowsWorkflow(t *testing.T) {
	assert.Equal(t, "Workflows workflow w", summarizeWorkflowsWorkflow(cloud.ResourceSpec{Name: "w"}))
}
