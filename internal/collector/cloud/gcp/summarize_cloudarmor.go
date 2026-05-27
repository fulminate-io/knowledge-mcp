// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:compute:securityPolicy", summarizeSecurityPolicy)
}

func summarizeSecurityPolicy(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Cloud Armor security policy", spec)
}
