// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Web/sites", summarizeAppServiceSite)
}

func summarizeAppServiceSite(spec cloud.ResourceSpec) string {
	return azureGenericSummary("App Service site", spec)
}
