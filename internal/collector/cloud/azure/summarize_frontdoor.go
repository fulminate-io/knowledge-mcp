// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Cdn/profiles", summarizeCDNProfile)
	cloud.Register("Microsoft.Cdn/profiles/afdEndpoints", summarizeAFDEndpoint)
}

func summarizeCDNProfile(spec cloud.ResourceSpec) string {
	return azureGenericSummary("CDN/Front Door profile", spec)
}

func summarizeAFDEndpoint(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Front Door endpoint", spec)
}
