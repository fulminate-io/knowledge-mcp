// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Compute/disks", summarizeAzureDisk)
}

func summarizeAzureDisk(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure managed disk", spec)
}
