// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Insights/metricAlerts", summarizeMetricAlert)
}

func summarizeMetricAlert(spec cloud.ResourceSpec) string {
	return azureGenericSummary("metric alert", spec)
}
