// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeLogicAppWorkflow(t *testing.T) {
	assert.Equal(t, "Logic App workflow w", summarizeLogicAppWorkflow(cloud.ResourceSpec{Name: "w"}))
}
