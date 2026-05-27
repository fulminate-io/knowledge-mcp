// SPDX-License-Identifier: Apache-2.0

package k8s

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("GatewayClass", summarizeGatewayClass)
	cloud.Register("Gateway", summarizeGateway)
	// HTTPRoute and similar kinds are emitted dynamically (sub_gatewayapi.go:185
	// ResourceType: kind); not registered as literals — fallback covers them.
}

func summarizeGatewayClass(spec cloud.ResourceSpec) string {
	return k8sClusterSummary("GatewayClass", spec)
}

func summarizeGateway(spec cloud.ResourceSpec) string {
	return k8sNamespacedSummary("Gateway", spec)
}
