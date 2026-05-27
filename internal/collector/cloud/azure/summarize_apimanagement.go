// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.ApiManagement/service", summarizeAPIMService)
	cloud.Register("Microsoft.ApiManagement/service/apis", summarizeAPIMAPI)
}

func summarizeAPIMService(spec cloud.ResourceSpec) string {
	return azureGenericSummary("API Management service", spec)
}

func summarizeAPIMAPI(spec cloud.ResourceSpec) string {
	return azureGenericSummary("API Management API", spec)
}
