// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Microsoft.Web/certificates", summarizeWebCertificate)
	cloud.Register("azure:ca", summarizeAzureCA)
}

func summarizeWebCertificate(spec cloud.ResourceSpec) string {
	return azureGenericSummary("App Service certificate", spec)
}

func summarizeAzureCA(spec cloud.ResourceSpec) string {
	return azureGenericSummary("Azure CA", spec)
}
