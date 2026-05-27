// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.ContainerRegistry/registries", summarizeACR)
}

func summarizeACR(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure Container Registry", spec)
}
