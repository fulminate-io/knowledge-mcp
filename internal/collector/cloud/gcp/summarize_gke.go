// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:container:cluster", summarizeGKECluster)
}

func summarizeGKECluster(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("GKE cluster", spec)
}
