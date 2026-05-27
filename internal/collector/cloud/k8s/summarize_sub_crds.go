// SPDX-License-Identifier: Apache-2.0

package k8s

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("CustomResourceDefinition", summarizeCRD)
	// Dynamic kinds (sub_crds.go:169 ResourceType: kind) are not literals; rely
	// on the runtime fallback for unknown CRD-derived kinds.
}

func summarizeCRD(spec cloud.ResourceSpec) string {
	return k8sClusterSummary("CRD", spec)
}
