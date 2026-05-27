// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type redisCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newRedisCollector(cred azcore.TokenCredential, subID string) *redisCollector {
	return &redisCollector{cred: cred, subscriptionID: subID}
}

func (c *redisCollector) Name() string { return "azure-redis" }

func (c *redisCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armredis.NewClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-redis: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListBySubscriptionPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-redis: list: %w", err)
		}

		for _, cache := range page.Value {
			if cache.ID == nil || cache.Name == nil {
				continue
			}

			content, err := json.Marshal(cache)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, redisResourceSpec(cache, content))
			result.Edges = append(result.Edges, redisEdges(cache)...)
		}
	}

	return result, nil
}

func redisResourceSpec(cache *armredis.ResourceInfo, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *cache.ID,
		Name:         *cache.Name,
		ResourceType: "Microsoft.Cache/redis",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if cache.Location != nil {
		spec.Region = *cache.Location
	}
	redisPropertiesMetadata(cache.Properties, spec.Metadata)
	return spec
}

func redisPropertiesMetadata(p *armredis.Properties, meta map[string]string) {
	if p == nil {
		return
	}
	if p.SKU != nil && p.SKU.Name != nil {
		meta["skuName"] = string(*p.SKU.Name)
	}
	if p.SKU != nil && p.SKU.Family != nil {
		meta["skuFamily"] = string(*p.SKU.Family)
	}
	if p.RedisVersion != nil {
		meta["redisVersion"] = *p.RedisVersion
	}
	if p.MinimumTLSVersion != nil {
		meta["minimumTlsVersion"] = string(*p.MinimumTLSVersion)
	}
	if p.PublicNetworkAccess != nil {
		meta["publicNetworkAccess"] = string(*p.PublicNetworkAccess)
	}
	if p.EnableNonSSLPort != nil {
		meta["enableNonSslPort"] = fmt.Sprintf("%t", *p.EnableNonSSLPort)
	}
	if p.ProvisioningState != nil {
		meta["provisioningState"] = string(*p.ProvisioningState)
	}
}

// redisEdges emits USES_SUBNET, USES_NETWORK (derived), and ASSUMES_ROLE edges
// for an Azure Cache for Redis instance. Note: customer-managed key encryption
// is not exposed on the armredis Properties struct — CMK is only available on
// the Enterprise tier which uses a separate SDK (armredisenterprise). Basic,
// Standard, and Premium tiers rely on Microsoft-managed encryption only, so
// no ENCRYPTS_WITH edge is emitted here.
func redisEdges(cache *armredis.ResourceInfo) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Edges: Redis → subnet (USES_SUBNET) + parent VNet (USES_NETWORK).
	// Premium-tier caches may be deployed into a customer subnet via SubnetID.
	if cache.Properties != nil && cache.Properties.SubnetID != nil && *cache.Properties.SubnetID != "" {
		subnetID := *cache.Properties.SubnetID
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *cache.ID,
			TargetID:     subnetID,
			Relationship: kgtypes.EdgeUsesSubnet,
		})
		if vnetID := vnetIDFromSubnet(subnetID); vnetID != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *cache.ID,
				TargetID:     vnetID,
				Relationship: kgtypes.EdgeUsesNetwork,
			})
		}
	}

	// Edges: Redis → managed identity (ASSUMES_ROLE) via user-assigned identities.
	if cache.Identity != nil && cache.Identity.UserAssignedIdentities != nil {
		for identityID := range cache.Identity.UserAssignedIdentities {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *cache.ID,
				TargetID:     identityID,
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     map[string]string{"role_source": "managed_identity"},
			})
		}
	}

	return edges
}
