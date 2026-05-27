// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/synapse/armsynapse"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectPools enumerates both dedicated SQL pools and Apache Spark (Big Data)
// pools nested under a Synapse workspace. Per decision, both pool types are
// collected for full coverage. Pools are emitted as resource nodes with a
// CONTAINS edge from the workspace.
func (c *synapseCollector) collectPools(
	ctx context.Context,
	sqlClient *armsynapse.SQLPoolsClient,
	bdpClient *armsynapse.BigDataPoolsClient,
	ws *armsynapse.Workspace,
	result *cloud.SubCollectorResult,
) {
	rg, wsName := parseSynapseWorkspaceID(*ws.ID)
	if rg == "" || wsName == "" {
		return
	}

	c.collectSQLPools(ctx, sqlClient, rg, wsName, ws, result)
	c.collectBigDataPools(ctx, bdpClient, rg, wsName, ws, result)
}

func (c *synapseCollector) collectSQLPools(
	ctx context.Context,
	client *armsynapse.SQLPoolsClient,
	resourceGroup, workspaceName string,
	ws *armsynapse.Workspace,
	result *cloud.SubCollectorResult,
) {
	pager := client.NewListByWorkspacePager(resourceGroup, workspaceName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return
		}
		for _, pool := range page.Value {
			if pool.ID == nil || pool.Name == nil {
				continue
			}
			content, err := json.Marshal(pool)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, synapseSQLPoolResourceSpec(pool, content))
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *ws.ID,
				TargetID:     *pool.ID,
				Relationship: kgtypes.EdgeContains,
			})
		}
	}
}

func (c *synapseCollector) collectBigDataPools(
	ctx context.Context,
	client *armsynapse.BigDataPoolsClient,
	resourceGroup, workspaceName string,
	ws *armsynapse.Workspace,
	result *cloud.SubCollectorResult,
) {
	pager := client.NewListByWorkspacePager(resourceGroup, workspaceName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return
		}
		for _, pool := range page.Value {
			if pool.ID == nil || pool.Name == nil {
				continue
			}
			content, err := json.Marshal(pool)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, synapseBigDataPoolResourceSpec(pool, content))
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *ws.ID,
				TargetID:     *pool.ID,
				Relationship: kgtypes.EdgeContains,
			})
		}
	}
}

func synapseSQLPoolResourceSpec(pool *armsynapse.SQLPool, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *pool.ID,
		Name:         *pool.Name,
		ResourceType: "Microsoft.Synapse/workspaces/sqlPools",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if pool.Location != nil {
		spec.Region = *pool.Location
	}
	if pool.SKU != nil && pool.SKU.Name != nil {
		spec.Metadata["skuName"] = *pool.SKU.Name
	}
	if pool.Properties != nil {
		if pool.Properties.Status != nil {
			spec.Metadata["status"] = *pool.Properties.Status
		}
		if pool.Properties.ProvisioningState != nil {
			spec.Metadata["provisioningState"] = *pool.Properties.ProvisioningState
		}
	}
	return spec
}

func synapseBigDataPoolResourceSpec(pool *armsynapse.BigDataPoolResourceInfo, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *pool.ID,
		Name:         *pool.Name,
		ResourceType: "Microsoft.Synapse/workspaces/bigDataPools",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if pool.Location != nil {
		spec.Region = *pool.Location
	}
	sparkPoolPropsMetadata(spec.Metadata, pool.Properties)
	return spec
}

func sparkPoolPropsMetadata(md map[string]string, props *armsynapse.BigDataPoolResourceProperties) {
	if props == nil {
		return
	}
	if props.SparkVersion != nil {
		md["sparkVersion"] = *props.SparkVersion
	}
	if props.NodeCount != nil {
		md["nodeCount"] = fmt.Sprintf("%d", *props.NodeCount)
	}
	if props.NodeSize != nil {
		md["nodeSize"] = string(*props.NodeSize)
	}
	if props.ProvisioningState != nil {
		md["provisioningState"] = *props.ProvisioningState
	}
}

// parseSynapseWorkspaceID extracts the resource group and workspace name from
// a Synapse workspace resource ID of the form:
//
//	/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Synapse/workspaces/{name}
func parseSynapseWorkspaceID(id string) (resourceGroup, workspaceName string) {
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			resourceGroup = parts[i+1]
		}
		if strings.EqualFold(parts[i], "workspaces") {
			workspaceName = parts[i+1]
		}
	}
	return resourceGroup, workspaceName
}
