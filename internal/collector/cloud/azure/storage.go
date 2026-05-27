// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type storageCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newStorageCollector(cred azcore.TokenCredential, subID string) *storageCollector {
	return &storageCollector{cred: cred, subscriptionID: subID}
}

func (c *storageCollector) Name() string { return "azure-storage" }

func (c *storageCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armstorage.NewAccountsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-storage: client: %w", err)
	}

	fileSharesClient, err := armstorage.NewFileSharesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-storage: fileshares client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-storage: list: %w", err)
		}

		for _, account := range page.Value {
			if account.ID == nil || account.Name == nil {
				continue
			}

			content, err := json.Marshal(account)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, storageResourceSpec(account, content))
			result.Edges = append(result.Edges, storageEdges(account)...)
			c.collectFileShares(ctx, fileSharesClient, account, &result)
		}
	}

	return result, nil
}

func storageResourceSpec(account *armstorage.Account, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *account.ID,
		Name:         *account.Name,
		ResourceType: "Microsoft.Storage/storageAccounts",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if account.Location != nil {
		spec.Region = *account.Location
	}
	if account.Kind != nil {
		spec.Metadata["kind"] = string(*account.Kind)
	}
	if account.SKU != nil && account.SKU.Name != nil {
		spec.Metadata["skuName"] = string(*account.SKU.Name)
	}
	if account.Properties != nil {
		if account.Properties.AccessTier != nil {
			spec.Metadata["accessTier"] = string(*account.Properties.AccessTier)
		}
		if account.Properties.EnableHTTPSTrafficOnly != nil {
			spec.Metadata["httpsTrafficOnly"] = fmt.Sprintf("%t", *account.Properties.EnableHTTPSTrafficOnly)
		}
	}
	return spec
}

func storageEdges(account *armstorage.Account) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Storage → Key Vault key (customer-managed encryption)
	edges = append(edges, storageEncryptionEdge(account)...)

	if account.Properties == nil || account.Properties.NetworkRuleSet == nil {
		return edges
	}
	for _, rule := range account.Properties.NetworkRuleSet.VirtualNetworkRules {
		if rule.VirtualNetworkResourceID != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *account.ID,
				TargetID:     *rule.VirtualNetworkResourceID,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
	}
	return edges
}

// storageEncryptionEdge returns an ENCRYPTS_WITH edge if the storage account
// is configured with a customer-managed Key Vault key.
func storageEncryptionEdge(account *armstorage.Account) []cloud.EdgeSpec {
	if account.Properties == nil || account.Properties.Encryption == nil {
		return nil
	}
	kvp := account.Properties.Encryption.KeyVaultProperties
	if kvp == nil || kvp.KeyVaultURI == nil || kvp.KeyName == nil {
		return nil
	}
	kvURI := *kvp.KeyVaultURI
	keyName := *kvp.KeyName
	if kvURI == "" || keyName == "" {
		return nil
	}
	targetURI := kvURI + "keys/" + keyName
	if kvp.KeyVersion != nil && *kvp.KeyVersion != "" {
		targetURI += "/" + *kvp.KeyVersion
	}
	return []cloud.EdgeSpec{{
		SourceID:     *account.ID,
		TargetID:     targetURI,
		Relationship: kgtypes.EdgeEncryptsWith,
	}}
}
