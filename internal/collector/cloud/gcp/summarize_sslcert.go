// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:compute:sslCertificate", summarizeSSLCertificate)
}

func summarizeSSLCertificate(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("SSL certificate", spec)
}
