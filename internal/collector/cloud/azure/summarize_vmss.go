// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Compute/virtualMachineScaleSets", summarizeAzureVMSS)
}

func summarizeAzureVMSS(spec cloud.ResourceSpec) string {
	return azureGenericSummary("VM Scale Set", spec)
}
