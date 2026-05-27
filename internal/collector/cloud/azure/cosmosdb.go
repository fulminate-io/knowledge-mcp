// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cosmos/armcosmos/v3"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type cosmosCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newCosmosCollector(cred azcore.TokenCredential, subID string) *cosmosCollector {
	return &cosmosCollector{cred: cred, subscriptionID: subID}
}

func (c *cosmosCollector) Name() string { return "azure-cosmosdb" }

func (c *cosmosCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armcosmos.NewDatabaseAccountsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-cosmosdb: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-cosmosdb: list: %w", err)
		}

		for _, account := range page.Value {
			if account.ID == nil || account.Name == nil {
				continue
			}

			content, err := json.Marshal(account)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, cosmosResourceSpec(account, content))
			result.Edges = append(result.Edges, cosmosEdges(account)...)
		}
	}

	return result, nil
}

func cosmosResourceSpec(account *armcosmos.DatabaseAccountGetResults, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *account.ID,
		Name:         *account.Name,
		ResourceType: "Microsoft.DocumentDB/databaseAccounts",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if account.Location != nil {
		spec.Region = *account.Location
	}
	if account.Kind != nil {
		spec.Metadata["kind"] = string(*account.Kind)
	}
	cosmosPropertiesMetadata(account.Properties, spec.Metadata)
	return spec
}

func cosmosPropertiesMetadata(p *armcosmos.DatabaseAccountGetProperties, meta map[string]string) {
	if p == nil {
		return
	}
	if p.DatabaseAccountOfferType != nil {
		meta["offerType"] = *p.DatabaseAccountOfferType
	}
	if p.ConsistencyPolicy != nil && p.ConsistencyPolicy.DefaultConsistencyLevel != nil {
		meta["consistencyLevel"] = string(*p.ConsistencyPolicy.DefaultConsistencyLevel)
	}
	if p.EnableMultipleWriteLocations != nil {
		meta["enableMultipleWriteLocations"] = fmt.Sprintf("%t", *p.EnableMultipleWriteLocations)
	}
}

func cosmosEdges(account *armcosmos.DatabaseAccountGetResults) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Edges: Cosmos DB → Key Vault key (customer-managed encryption)
	if account.Properties != nil && account.Properties.KeyVaultKeyURI != nil && *account.Properties.KeyVaultKeyURI != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *account.ID,
			TargetID:     *account.Properties.KeyVaultKeyURI,
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}

	// Edges: Cosmos DB → subnet (USES_SUBNET) via VirtualNetworkRules.
	if account.Properties != nil {
		for _, rule := range account.Properties.VirtualNetworkRules {
			if rule.ID != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     *account.ID,
					TargetID:     *rule.ID,
					Relationship: kgtypes.EdgeUsesSubnet,
				})
			}
		}
	}

	// Edges: Cosmos DB → managed identity (ASSUMES_ROLE)
	if account.Identity != nil && account.Identity.UserAssignedIdentities != nil {
		for identityID := range account.Identity.UserAssignedIdentities {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *account.ID,
				TargetID:     identityID,
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     map[string]string{"role_source": "managed_identity"},
			})
		}
	}

	return edges
}
