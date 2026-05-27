// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeAppInsightsComponent(t *testing.T) {
	assert.Equal(t, "Application Insights component c", summarizeAppInsightsComponent(cloud.ResourceSpec{Name: "c"}))
}

func TestSummarizeLogAnalyticsWorkspace(t *testing.T) {
	assert.Equal(t, "Log Analytics workspace ws", summarizeLogAnalyticsWorkspace(cloud.ResourceSpec{Name: "ws"}))
}
