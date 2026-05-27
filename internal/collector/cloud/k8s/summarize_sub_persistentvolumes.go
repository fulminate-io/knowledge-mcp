// SPDX-License-Identifier: Apache-2.0

package k8s

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("PersistentVolume", summarizePersistentVolume)
}

func summarizePersistentVolume(spec cloud.ResourceSpec) string {
	return k8sClusterSummary("PersistentVolume", spec)
}
