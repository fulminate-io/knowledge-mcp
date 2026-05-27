// SPDX-License-Identifier: Apache-2.0

package k8s

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Role", summarizeRole)
	cloud.Register("ClusterRole", summarizeClusterRole)
}

func summarizeRole(spec cloud.ResourceSpec) string {
	return k8sNamespacedSummary("Role", spec)
}

func summarizeClusterRole(spec cloud.ResourceSpec) string {
	return k8sClusterSummary("ClusterRole", spec)
}
