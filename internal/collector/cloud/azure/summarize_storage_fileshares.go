// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Storage/storageAccounts/fileServices/shares", summarizeStorageFileShare)
}

func summarizeStorageFileShare(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Storage file share", spec)
}
