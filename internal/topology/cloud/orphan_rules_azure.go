// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// orphan_rules_azure.go registers Azure orphan rules.
//
//   - Microsoft.Network/loadBalancers → no outgoing TARGETS edges (1.0)
//   - Microsoft.KeyVault/vaults → no outgoing ACCESSED_BY edges (0.9)
//   - Microsoft.Network/networkSecurityGroups → no outgoing CONTAINS edges (0.9)
//   - Microsoft.Web/certificates → no outgoing STORED_IN or ISSUED_BY edges (0.8)
//
// LOAD BALANCER — cloud/azure/loadbalancers.go iterates each LB's backend
// address pool and emits LB → NIC (or LB → VNet) edges via EdgeTargets.
// A load balancer with no backend addresses configured produces zero
// outbound TARGETS edges and is orphaned: it occupies a public IP and
// SKU billing line without forwarding any traffic.

const (
	confidenceAzureLoadBalancer = 1.0
	confidenceAzureKeyVault     = 0.9
	confidenceAzureNSG          = 0.9
	confidenceAzureCertificate  = 0.8
	confidenceAzureAADGroup     = 0.9
)

// azureLoadBalancerRule reports an Azure load balancer as orphaned when it
// has no outbound TARGETS edge. The collector emits one edge per backend
// address (NIC IP config or VNet); a fully empty backend pool means zero
// outbound TARGETS edges, which is the rule's orphan condition.
func azureLoadBalancerRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeTargets) {
		return false, confidenceAzureLoadBalancer, "", nil
	}
	return true, confidenceAzureLoadBalancer,
		fmt.Sprintf("Azure load balancer %s has no backend pool targets.", displayName(node)),
		nil
}

// azureKeyVaultRule reports an Azure Key Vault as orphaned when it has no
// outbound ACCESSED_BY edge. The collector emits one edge per legacy access
// policy principal and one per RBAC role assignment; a vault with zero
// ACCESSED_BY edges has no configured access and is likely unused or
// misconfigured.
func azureKeyVaultRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeAccessedBy) {
		return false, confidenceAzureKeyVault, "", nil
	}
	return true, confidenceAzureKeyVault,
		fmt.Sprintf("Azure Key Vault %s has no access policies or RBAC assignments.", displayName(node)),
		nil
}

// azureNSGRule reports an Azure NSG as orphaned when it has no outbound
// CONTAINS edge. The collector emits one edge per security rule; an NSG with
// zero rules (user-defined and default) is likely misconfigured.
func azureNSGRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeContains) {
		return false, confidenceAzureNSG, "", nil
	}
	return true, confidenceAzureNSG,
		fmt.Sprintf("Azure NSG %s has no security rules.", displayName(node)),
		nil
}

// azureCertificateRule reports an App Service certificate as orphaned when it
// has no outbound STORED_IN or ISSUED_BY edge. The collector emits STORED_IN
// when KeyVaultID is set and ISSUED_BY when Issuer is set. A cert with neither
// is completely isolated.
func azureCertificateRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeStoredIn) {
		return false, confidenceAzureCertificate, "", nil
	}
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeIssuedBy) {
		return false, confidenceAzureCertificate, "", nil
	}
	return true, confidenceAzureCertificate,
		fmt.Sprintf("Azure certificate %s has no Key Vault or issuer relationships.", displayName(node)),
		nil
}

// azureAADGroupRule reports an Azure AD group as orphaned when it has no
// outbound HAS_MEMBER edges. The AAD groups subcollector emits one edge per
// member; a group with zero members is likely stale or misconfigured.
func azureAADGroupRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeHasMember) {
		return false, confidenceAzureAADGroup, "", nil
	}
	return true, confidenceAzureAADGroup,
		fmt.Sprintf("Azure AD group %s has no members.", displayName(node)),
		nil
}

// init self-registers the Azure orphan rules. Resource type strings match
// the values emitted by cloud/azure/ subcollectors.
func init() {
	registerOrphanRule("Microsoft.Network/loadBalancers", azureLoadBalancerRule)
	registerOrphanRule("Microsoft.KeyVault/vaults", azureKeyVaultRule)
	registerOrphanRule("Microsoft.Network/networkSecurityGroups", azureNSGRule)
	registerOrphanRule("Microsoft.Web/certificates", azureCertificateRule)
	registerOrphanRule("azure:aad:group", azureAADGroupRule)
}
