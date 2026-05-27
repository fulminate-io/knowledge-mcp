// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:compute:nat", summarizeCloudNAT)
}

func summarizeCloudNAT(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Cloud NAT", spec)
}
