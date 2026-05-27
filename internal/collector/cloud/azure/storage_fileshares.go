// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectFileShares enumerates Azure File Shares nested under a storage account
// and appends share nodes plus CONTAINS edges (account → share) to result.
// Errors are logged to stderr but do not abort sibling-account collection —
// file shares are optional enrichment and a missing RBAC grant or disabled
// file service should not fail storage collection.
func (c *storageCollector) collectFileShares(
	ctx context.Context,
	client *armstorage.FileSharesClient,
	account *armstorage.Account,
	result *cloud.SubCollectorResult,
) {
	if account == nil || account.ID == nil {
		return
	}
	rg, accountName := parseStorageAccountID(*account.ID)
	if rg == "" || accountName == "" {
		return
	}

	pager := client.NewListPager(rg, accountName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			// Best-effort: stop listing for this account but continue overall.
			return
		}
		for _, share := range page.Value {
			if share.ID == nil || share.Name == nil {
				continue
			}

			content, err := json.Marshal(share)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, fileShareResourceSpec(account, share, content))
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *account.ID,
				TargetID:     *share.ID,
				Relationship: kgtypes.EdgeContains,
			})
		}
	}
}

func fileShareResourceSpec(account *armstorage.Account, share *armstorage.FileShareItem, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *share.ID,
		Name:         *share.Name,
		ResourceType: "Microsoft.Storage/storageAccounts/fileServices/shares",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if account != nil && account.Location != nil {
		spec.Region = *account.Location
	}
	fileSharePropertiesMetadata(share.Properties, spec.Metadata)
	return spec
}

func fileSharePropertiesMetadata(p *armstorage.FileShareProperties, meta map[string]string) {
	if p == nil {
		return
	}
	if p.ShareQuota != nil {
		meta["shareQuotaGiB"] = fmt.Sprintf("%d", *p.ShareQuota)
	}
	if p.AccessTier != nil {
		meta["accessTier"] = string(*p.AccessTier)
	}
	if p.EnabledProtocols != nil {
		meta["enabledProtocols"] = string(*p.EnabledProtocols)
	}
	if p.RootSquash != nil {
		meta["rootSquash"] = string(*p.RootSquash)
	}
}

// parseStorageAccountID extracts the resource group and storage account name
// from a storage account resource ID of the form:
//
//	/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Storage/storageAccounts/{name}
func parseStorageAccountID(id string) (resourceGroup, accountName string) {
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			resourceGroup = parts[i+1]
		}
		if strings.EqualFold(parts[i], "storageAccounts") {
			accountName = parts[i+1]
		}
	}
	return resourceGroup, accountName
}
