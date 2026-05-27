// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type disksCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newDisksCollector(cred azcore.TokenCredential, subID string) *disksCollector {
	return &disksCollector{cred: cred, subscriptionID: subID}
}

func (c *disksCollector) Name() string { return "azure-disks" }

func (c *disksCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armcompute.NewDisksClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-disks: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-disks: list: %w", err)
		}

		for _, disk := range page.Value {
			if disk.ID == nil || disk.Name == nil {
				continue
			}

			content, err := json.Marshal(disk)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, diskResourceSpec(disk, content))
			result.Edges = append(result.Edges, diskEdges(disk)...)
		}
	}

	return result, nil
}

func diskResourceSpec(disk *armcompute.Disk, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *disk.ID,
		Name:         *disk.Name,
		ResourceType: "Microsoft.Compute/disks",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if disk.Location != nil {
		spec.Region = *disk.Location
	}
	if disk.SKU != nil && disk.SKU.Name != nil {
		spec.Metadata["skuName"] = string(*disk.SKU.Name)
	}
	if disk.Properties != nil && disk.Properties.DiskSizeGB != nil {
		spec.Metadata["diskSizeGB"] = fmt.Sprintf("%d", *disk.Properties.DiskSizeGB)
	}
	return spec
}

// diskEdges emits BOUND_TO (disk → managing VM) and ENCRYPTS_WITH (disk →
// disk encryption set) edges.
func diskEdges(disk *armcompute.Disk) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Disk BOUND_TO the VM that manages it.
	if disk.ManagedBy != nil && *disk.ManagedBy != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *disk.ID,
			TargetID:     *disk.ManagedBy,
			Relationship: kgtypes.EdgeBoundTo,
		})
	}

	// Disk ENCRYPTS_WITH disk encryption set.
	if disk.Properties != nil && disk.Properties.Encryption != nil {
		desID := disk.Properties.Encryption.DiskEncryptionSetID
		if desID != nil && *desID != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *disk.ID,
				TargetID:     *desID,
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}
	}

	return edges
}
