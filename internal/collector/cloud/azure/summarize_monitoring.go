// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Insights/components", summarizeAppInsightsComponent)
	cloud.Register("Microsoft.OperationalInsights/workspaces", summarizeLogAnalyticsWorkspace)
}

func summarizeAppInsightsComponent(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Application Insights component", spec)
}

func summarizeLogAnalyticsWorkspace(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Log Analytics workspace", spec)
}
