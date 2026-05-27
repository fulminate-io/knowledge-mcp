// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Search/searchServices", summarizeSearchService)
}

func summarizeSearchService(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure Search service", spec)
}
