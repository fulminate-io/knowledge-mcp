// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("azure:aad:group", summarizeAADGroup)
}

func summarizeAADGroup(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure AD group", spec)
}
