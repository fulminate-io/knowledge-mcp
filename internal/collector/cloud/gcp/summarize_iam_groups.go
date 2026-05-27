// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:cloudidentity:group", summarizeCloudIdentityGroup)
}

func summarizeCloudIdentityGroup(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Cloud Identity group", spec)
}
