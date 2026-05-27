// SPDX-License-Identifier: Apache-2.0

// Package-level summarizer registrations for Kubernetes resource types. K8s
// uses bare CamelCase Kind strings as the registry key (Pod, Deployment,
// Service, etc.). Helpers MUST read namespace from spec.Metadata["namespace"]
// not spec.Region (the plan locks helpers to Metadata-only); namespaced
// sub_*.go files already populate meta["namespace"] alongside Region.
package k8s

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// k8sNamespacedSummary returns a generic summary for a namespaced K8s
// resource: "<prefix> <name> in <namespace>".
func k8sNamespacedSummary(prefix string, spec cloud.ResourceSpec) string {
	parts := []string{prefix, spec.Name}
	if ns := spec.Metadata["namespace"]; ns != "" {
		parts = append(parts, fmt.Sprintf("in %s", ns))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// k8sClusterSummary returns a generic summary for a cluster-scoped K8s
// resource: "<prefix> <name>".
func k8sClusterSummary(prefix string, spec cloud.ResourceSpec) string {
	return strings.TrimSpace(fmt.Sprintf("%s %s", prefix, spec.Name))
}
