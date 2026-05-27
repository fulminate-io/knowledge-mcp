// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type aksCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newAKSCollector(cred azcore.TokenCredential, subID string) *aksCollector {
	return &aksCollector{cred: cred, subscriptionID: subID}
}

func (c *aksCollector) Name() string { return "azure-aks" }

func (c *aksCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armcontainerservice.NewManagedClustersClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-aks: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-aks: list: %w", err)
		}

		for _, cluster := range page.Value {
			if cluster.ID == nil || cluster.Name == nil {
				continue
			}

			content, err := json.Marshal(cluster)
			if err != nil {
				continue
			}

			spec := aksResourceSpec(cluster, content)
			result.Resources = append(result.Resources, spec)
			result.Edges = append(result.Edges, aksEdges(cluster)...)

			if ctx := aksKubecontext(cluster); ctx != "" {
				result.Targets = append(result.Targets, cloud.CollectTarget{
					Collector:    "k8s",
					ID:           ctx,
					ResolutionID: *cluster.ID,
				})
			}
		}
	}

	return result, nil
}

func aksResourceSpec(cluster *armcontainerservice.ManagedCluster, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *cluster.ID,
		Name:         *cluster.Name,
		ResourceType: "Microsoft.ContainerService/managedClusters",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if cluster.Location != nil {
		spec.Region = *cluster.Location
	}
	aksPropertiesMetadata(cluster.Properties, spec.Metadata)
	return spec
}

func aksPropertiesMetadata(p *armcontainerservice.ManagedClusterProperties, meta map[string]string) {
	if p == nil {
		return
	}
	if p.KubernetesVersion != nil {
		meta["kubernetesVersion"] = *p.KubernetesVersion
	}
	if p.NodeResourceGroup != nil {
		meta["nodeResourceGroup"] = *p.NodeResourceGroup
	}
	if p.Fqdn != nil {
		meta["fqdn"] = *p.Fqdn
	}
	if p.PowerState != nil && p.PowerState.Code != nil {
		meta["powerState"] = string(*p.PowerState.Code)
	}
}

func aksEdges(cluster *armcontainerservice.ManagedCluster) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	if p := cluster.Properties; p != nil {
		for _, pool := range p.AgentPoolProfiles {
			if pool.VnetSubnetID != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID: *cluster.ID, TargetID: *pool.VnetSubnetID, Relationship: kgtypes.EdgeUsesSubnet,
				})
			}
		}
	}
	if cluster.Identity != nil {
		for identityID := range cluster.Identity.UserAssignedIdentities {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *cluster.ID,
				TargetID:     identityID,
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     map[string]string{"role_source": "managed_identity"},
			})
		}
	}
	return edges
}

// aksKubecontext builds the kubeconfig context name for an AKS cluster.
// Azure CLI generates contexts in the form: clusterName (the simple form used by az aks get-credentials).
func aksKubecontext(cluster *armcontainerservice.ManagedCluster) string {
	if cluster.Name == nil {
		return ""
	}
	return *cluster.Name
}
