// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type acrCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newACRCollector(cred azcore.TokenCredential, subID string) *acrCollector {
	return &acrCollector{cred: cred, subscriptionID: subID}
}

func (c *acrCollector) Name() string { return "azure-containerregistry" }

func (c *acrCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armcontainerregistry.NewRegistriesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-containerregistry: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-containerregistry: list: %w", err)
		}

		for _, reg := range page.Value {
			if reg.ID == nil || reg.Name == nil {
				continue
			}

			content, err := json.Marshal(buildRegistryContent(reg))
			if err != nil {
				return result, fmt.Errorf("azure-containerregistry: marshal registry content: %w", err)
			}

			result.Resources = append(result.Resources, acrResourceSpec(reg, content))
			result.Edges = append(result.Edges, acrEdges(reg)...)
		}
	}

	return result, nil
}

// registryContent is the curated wire shape for
// Microsoft.ContainerRegistry/registries. Curated projection of
// *armcontainerregistry.Registry (collector-owned, decoupled from SDK
// version). Convergence target for extractACRLoginServer at
// postpopulate_images.go:93 — Properties is a non-pointer struct because
// the existing reader expects properties to be present.
//
// Excluded: PrivateEndpointConnections — pre-Marshal at
// containerregistry.go:93.
type registryContent struct {
	ID         string                    `json:"id"`
	Name       string                    `json:"name"`
	Location   string                    `json:"location,omitempty"`
	SKU        *registryContentSKU       `json:"sku,omitempty"`
	Properties registryContentProperties `json:"properties"`
}

type registryContentSKU struct {
	Name string `json:"name,omitempty"`
}

type registryContentProperties struct {
	LoginServer       string `json:"loginServer,omitempty"`
	ProvisioningState string `json:"provisioningState,omitempty"`
}

// buildRegistryContent projects an *armcontainerregistry.Registry into the
// registryContent wire shape. Nil-safe at every level.
func buildRegistryContent(reg *armcontainerregistry.Registry) registryContent {
	out := registryContent{}
	if reg == nil {
		return out
	}
	if reg.ID != nil {
		out.ID = *reg.ID
	}
	if reg.Name != nil {
		out.Name = *reg.Name
	}
	if reg.Location != nil {
		out.Location = *reg.Location
	}
	if reg.SKU != nil && reg.SKU.Name != nil {
		out.SKU = &registryContentSKU{Name: string(*reg.SKU.Name)}
	}
	if reg.Properties != nil {
		if reg.Properties.LoginServer != nil {
			out.Properties.LoginServer = *reg.Properties.LoginServer
		}
		if reg.Properties.ProvisioningState != nil {
			out.Properties.ProvisioningState = string(*reg.Properties.ProvisioningState)
		}
	}
	return out
}

func acrResourceSpec(reg *armcontainerregistry.Registry, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *reg.ID,
		Name:         *reg.Name,
		ResourceType: "Microsoft.ContainerRegistry/registries",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if reg.Location != nil {
		spec.Region = *reg.Location
	}
	if reg.SKU != nil && reg.SKU.Name != nil {
		spec.Metadata["skuName"] = string(*reg.SKU.Name)
	}
	if reg.Properties != nil {
		if reg.Properties.LoginServer != nil {
			spec.Metadata["loginServer"] = *reg.Properties.LoginServer
		}
		if reg.Properties.ProvisioningState != nil {
			spec.Metadata["provisioningState"] = string(*reg.Properties.ProvisioningState)
		}
	}
	return spec
}

func acrEdges(reg *armcontainerregistry.Registry) []cloud.EdgeSpec {
	if reg.Properties == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	// Edges: ACR → private endpoint (USES_SUBNET)
	// ACR uses PrivateEndpointConnections for VNet integration (not VirtualNetworkRules).
	for _, conn := range reg.Properties.PrivateEndpointConnections {
		if conn.Properties != nil && conn.Properties.PrivateEndpoint != nil && conn.Properties.PrivateEndpoint.ID != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *reg.ID,
				TargetID:     *conn.Properties.PrivateEndpoint.ID,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
	}
	return edges
}
