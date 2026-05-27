// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type sqlCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newSQLCollector(cred azcore.TokenCredential, subID string) *sqlCollector {
	return &sqlCollector{cred: cred, subscriptionID: subID}
}

func (c *sqlCollector) Name() string { return "azure-sql" }

func (c *sqlCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	serversClient, err := armsql.NewServersClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-sql: servers client: %w", err)
	}

	dbsClient, err := armsql.NewDatabasesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-sql: databases client: %w", err)
	}

	peClient, err := armsql.NewPrivateEndpointConnectionsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-sql: pe client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := serversClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-sql: list servers: %w", err)
		}

		for _, server := range page.Value {
			if server.ID == nil || server.Name == nil {
				continue
			}

			content, err := json.Marshal(server)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, sqlServerResourceSpec(server, content))
			result.Edges = append(result.Edges, sqlServerEdges(ctx, server, peClient)...)
			c.collectDatabases(ctx, dbsClient, server, &result)
		}
	}

	return result, nil
}

func sqlServerResourceSpec(server *armsql.Server, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *server.ID,
		Name:         *server.Name,
		ResourceType: "Microsoft.Sql/servers",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if server.Location != nil {
		spec.Region = *server.Location
	}
	if server.Properties != nil {
		if server.Properties.FullyQualifiedDomainName != nil {
			spec.Metadata["fqdn"] = *server.Properties.FullyQualifiedDomainName
		}
		if server.Properties.State != nil {
			spec.Metadata["state"] = *server.Properties.State
		}
		if server.Properties.Version != nil {
			spec.Metadata["version"] = *server.Properties.Version
		}
	}
	return spec
}

func sqlServerEdges(ctx context.Context, server *armsql.Server, peClient *armsql.PrivateEndpointConnectionsClient) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Edges: SQL server → managed identity (ASSUMES_ROLE)
	if server.Identity != nil && server.Identity.UserAssignedIdentities != nil {
		for identityID := range server.Identity.UserAssignedIdentities {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *server.ID,
				TargetID:     identityID,
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     map[string]string{"role_source": "managed_identity"},
			})
		}
	}

	// Edges: SQL server → Key Vault key (TDE encryption with CMK)
	if server.Properties != nil && server.Properties.KeyID != nil && *server.Properties.KeyID != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *server.ID,
			TargetID:     *server.Properties.KeyID,
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}

	// Edges: SQL server → private endpoint (USES_SUBNET)
	// Per decision: edges point to the PE resource ID directly, not deeper subnet.
	rg, serverName := parseServerID(*server.ID)
	if rg != "" && serverName != "" {
		pePager := peClient.NewListByServerPager(rg, serverName, nil)
		for pePager.More() {
			pePage, err := pePager.NextPage(ctx)
			if err != nil {
				break
			}
			for _, conn := range pePage.Value {
				if conn.Properties != nil && conn.Properties.PrivateEndpoint != nil && conn.Properties.PrivateEndpoint.ID != nil {
					edges = append(edges, cloud.EdgeSpec{
						SourceID:     *server.ID,
						TargetID:     *conn.Properties.PrivateEndpoint.ID,
						Relationship: kgtypes.EdgeUsesSubnet,
					})
				}
			}
		}
	}

	return edges
}

func (c *sqlCollector) collectDatabases(ctx context.Context, dbsClient *armsql.DatabasesClient, server *armsql.Server, result *cloud.SubCollectorResult) {
	rg, serverName := parseServerID(*server.ID)
	if rg == "" || serverName == "" {
		return
	}

	dbPager := dbsClient.NewListByServerPager(rg, serverName, nil)
	for dbPager.More() {
		dbPage, err := dbPager.NextPage(ctx)
		if err != nil {
			// Best-effort: log and continue with next server.
			break
		}

		for _, db := range dbPage.Value {
			if db.ID == nil || db.Name == nil {
				continue
			}

			dbContent, err := json.Marshal(db)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, sqlDBResourceSpec(db, dbContent))
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     *server.ID,
				TargetID:     *db.ID,
				Relationship: kgtypes.EdgeContains,
			})
		}
	}
}

func sqlDBResourceSpec(db *armsql.Database, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *db.ID,
		Name:         *db.Name,
		ResourceType: "Microsoft.Sql/servers/databases",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if db.Location != nil {
		spec.Region = *db.Location
	}
	if db.SKU != nil && db.SKU.Name != nil {
		spec.Metadata["skuName"] = *db.SKU.Name
	}
	if db.Properties != nil && db.Properties.Status != nil {
		spec.Metadata["status"] = string(*db.Properties.Status)
	}
	return spec
}

// parseServerID extracts the resource group name and server name from an
// Azure SQL server resource ID of the form:
// /subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Sql/servers/{name}
func parseServerID(id string) (resourceGroup, serverName string) {
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	// Expected: subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Sql/servers/{name}
	// Indices:  0              1    2               3   4          5            6  7       8
	if len(parts) < 8 {
		return "", ""
	}
	for i := 0; i < len(parts)-1; i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			resourceGroup = parts[i+1]
		}
		if strings.EqualFold(parts[i], "servers") {
			serverName = parts[i+1]
		}
	}
	return resourceGroup, serverName
}
