// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Insights/diagnosticSettings", summarizeDiagnosticSetting)
}

func summarizeDiagnosticSetting(spec cloud.ResourceSpec) string {
	return azureGenericSummary("diagnostic setting", spec)
}
