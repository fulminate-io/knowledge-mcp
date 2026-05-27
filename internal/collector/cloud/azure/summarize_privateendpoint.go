// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Network/privateEndpoints", summarizePrivateEndpoint)
}

func summarizePrivateEndpoint(spec cloud.ResourceSpec) string {
	return azureGenericSummary("private endpoint", spec)
}
