// SPDX-License-Identifier: Apache-2.0

package k8s

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("RoleBinding", summarizeRoleBinding)
	cloud.Register("ClusterRoleBinding", summarizeClusterRoleBinding)
}

func summarizeRoleBinding(spec cloud.ResourceSpec) string {
	return k8sNamespacedSummary("RoleBinding", spec)
}

func summarizeClusterRoleBinding(spec cloud.ResourceSpec) string {
	return k8sClusterSummary("ClusterRoleBinding", spec)
}
