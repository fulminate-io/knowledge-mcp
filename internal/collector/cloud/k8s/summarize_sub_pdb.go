// SPDX-License-Identifier: Apache-2.0

package k8s

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("PodDisruptionBudget", summarizePDB)
}

func summarizePDB(spec cloud.ResourceSpec) string {
	return k8sNamespacedSummary("PodDisruptionBudget", spec)
}
