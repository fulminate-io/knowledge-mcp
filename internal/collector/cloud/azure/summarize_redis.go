// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Cache/redis", summarizeAzureCache)
}

func summarizeAzureCache(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure Cache for Redis", spec)
}
