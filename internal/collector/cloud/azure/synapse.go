// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/synapse/armsynapse"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type synapseCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newSynapseCollector(cred azcore.TokenCredential, subID string) *synapseCollector {
	return &synapseCollector{cred: cred, subscriptionID: subID}
}

func (c *synapseCollector) Name() string { return "azure-synapse" }

func (c *synapseCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	wsClient, err := armsynapse.NewWorkspacesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-synapse: workspaces client: %w", err)
	}
	sqlPoolsClient, err := armsynapse.NewSQLPoolsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-synapse: sqlpools client: %w", err)
	}
	bigDataPoolsClient, err := armsynapse.NewBigDataPoolsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-synapse: bigdatapools client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := wsClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-synapse: list workspaces: %w", err)
		}

		for _, ws := range page.Value {
			if ws.ID == nil || ws.Name == nil {
				continue
			}
			content, err := json.Marshal(ws)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, synapseWorkspaceResourceSpec(ws, content))
			result.Edges = append(result.Edges, synapseWorkspaceEdges(ws)...)
			c.collectPools(ctx, sqlPoolsClient, bigDataPoolsClient, ws, &result)
		}
	}

	return result, nil
}

func synapseWorkspaceResourceSpec(ws *armsynapse.Workspace, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *ws.ID,
		Name:         *ws.Name,
		ResourceType: "Microsoft.Synapse/workspaces",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if ws.Location != nil {
		spec.Region = *ws.Location
	}
	synapseWorkspacePropertiesMetadata(ws.Properties, spec.Metadata)
	return spec
}

func synapseWorkspacePropertiesMetadata(p *armsynapse.WorkspaceProperties, meta map[string]string) {
	if p == nil {
		return
	}
	if p.ProvisioningState != nil {
		meta["provisioningState"] = *p.ProvisioningState
	}
	if p.PublicNetworkAccess != nil {
		meta["publicNetworkAccess"] = string(*p.PublicNetworkAccess)
	}
	if p.ManagedVirtualNetwork != nil {
		meta["managedVirtualNetwork"] = *p.ManagedVirtualNetwork
	}
	if p.SQLAdministratorLogin != nil {
		meta["sqlAdministratorLogin"] = *p.SQLAdministratorLogin
	}
}

// synapseWorkspaceEdges emits USES_SUBNET + USES_NETWORK for VirtualNetworkProfile,
// ENCRYPTS_WITH for CMK, and ASSUMES_ROLE for managed identities.
func synapseWorkspaceEdges(ws *armsynapse.Workspace) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Edges: Workspace → managed identity (ASSUMES_ROLE)
	if ws.Identity != nil && ws.Identity.UserAssignedIdentities != nil {
		for identityID := range ws.Identity.UserAssignedIdentities {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *ws.ID,
				TargetID:     identityID,
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     map[string]string{"role_source": "managed_identity"},
			})
		}
	}

	if ws.Properties == nil {
		return edges
	}

	// Edges: Workspace → compute subnet (USES_SUBNET) + derived VNet (USES_NETWORK)
	if vnp := ws.Properties.VirtualNetworkProfile; vnp != nil && vnp.ComputeSubnetID != nil && *vnp.ComputeSubnetID != "" {
		subnetID := *vnp.ComputeSubnetID
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *ws.ID,
			TargetID:     subnetID,
			Relationship: kgtypes.EdgeUsesSubnet,
		})
		if vnetID := vnetIDFromSubnet(subnetID); vnetID != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *ws.ID,
				TargetID:     vnetID,
				Relationship: kgtypes.EdgeUsesNetwork,
			})
		}
	}

	// Edges: Workspace → Key Vault key (ENCRYPTS_WITH) via CMK
	if enc := ws.Properties.Encryption; enc != nil && enc.Cmk != nil && enc.Cmk.Key != nil {
		if kvURL := enc.Cmk.Key.KeyVaultURL; kvURL != nil && *kvURL != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *ws.ID,
				TargetID:     *kvURL,
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}
	}

	return edges
}
