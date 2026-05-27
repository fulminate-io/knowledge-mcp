// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Compute/virtualMachines", summarizeAzureVM)
}

func summarizeAzureVM(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure VM", spec)
}
