// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.ContainerService/managedClusters", summarizeAKSCluster)
}

func summarizeAKSCluster(spec cloud.ResourceSpec) string {
	return azureGenericSummary("AKS cluster", spec)
}
