// SPDX-License-Identifier: Apache-2.0

package k8s

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Node", summarizeNode)
}

func summarizeNode(spec cloud.ResourceSpec) string {
	return k8sClusterSummary("Node", spec)
}
