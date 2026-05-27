// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/search/armsearch"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type searchCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newSearchCollector(cred azcore.TokenCredential, subID string) *searchCollector {
	return &searchCollector{cred: cred, subscriptionID: subID}
}

func (c *searchCollector) Name() string { return "azure-search" }

func (c *searchCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armsearch.NewServicesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-search: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListBySubscriptionPager(nil, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-search: list: %w", err)
		}

		for _, svc := range page.Value {
			if svc.ID == nil || svc.Name == nil {
				continue
			}

			content, err := json.Marshal(svc)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, searchResourceSpec(svc, content))
			result.Edges = append(result.Edges, searchEdges(svc)...)
		}
	}

	return result, nil
}

func searchResourceSpec(svc *armsearch.Service, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *svc.ID,
		Name:         *svc.Name,
		ResourceType: "Microsoft.Search/searchServices",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if svc.Location != nil {
		spec.Region = *svc.Location
	}
	if svc.SKU != nil && svc.SKU.Name != nil {
		spec.Metadata["skuName"] = string(*svc.SKU.Name)
	}
	searchPropertiesMetadata(svc.Properties, spec.Metadata)
	return spec
}

func searchPropertiesMetadata(p *armsearch.ServiceProperties, meta map[string]string) {
	if p == nil {
		return
	}
	if p.ReplicaCount != nil {
		meta["replicaCount"] = fmt.Sprintf("%d", *p.ReplicaCount)
	}
	if p.PartitionCount != nil {
		meta["partitionCount"] = fmt.Sprintf("%d", *p.PartitionCount)
	}
	if p.PublicNetworkAccess != nil {
		meta["publicNetworkAccess"] = string(*p.PublicNetworkAccess)
	}
	if p.HostingMode != nil {
		meta["hostingMode"] = string(*p.HostingMode)
	}
	if p.ProvisioningState != nil {
		meta["provisioningState"] = string(*p.ProvisioningState)
	}
	if p.EncryptionWithCmk != nil && p.EncryptionWithCmk.Enforcement != nil {
		meta["cmkEnforcement"] = string(*p.EncryptionWithCmk.Enforcement)
	}
}

// searchEdges emits:
//   - ASSUMES_ROLE for each user-assigned managed identity
//   - USES_SUBNET for each connected private endpoint (private endpoint ID)
//
// Note: Azure AI Search CMK is configured per-index via the data-plane API
// (not on the service resource itself), so no ENCRYPTS_WITH edge is emitted
// here. NetworkRuleSet only exposes IP allow-lists, not VNet integration.
func searchEdges(svc *armsearch.Service) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Edges: Search service → managed identity (ASSUMES_ROLE)
	if svc.Identity != nil && svc.Identity.UserAssignedIdentities != nil {
		for identityID := range svc.Identity.UserAssignedIdentities {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *svc.ID,
				TargetID:     identityID,
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     map[string]string{"role_source": "managed_identity"},
			})
		}
	}

	// Edges: Search service → private endpoint (USES_SUBNET)
	// Per the Azure SQL pattern, edges point to the PE resource ID directly,
	// not the deeper subnet — postpopulate can walk PE → NIC → subnet later.
	if svc.Properties != nil {
		for _, conn := range svc.Properties.PrivateEndpointConnections {
			if conn == nil || conn.Properties == nil || conn.Properties.PrivateEndpoint == nil {
				continue
			}
			if conn.Properties.PrivateEndpoint.ID == nil || *conn.Properties.PrivateEndpoint.ID == "" {
				continue
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *svc.ID,
				TargetID:     *conn.Properties.PrivateEndpoint.ID,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
	}

	return edges
}
