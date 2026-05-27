// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	container "cloud.google.com/go/container/apiv1"
	containerpb "cloud.google.com/go/container/apiv1/containerpb"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// gkeSubCollector collects GKE clusters across all locations.
type gkeSubCollector struct {
	client          *container.ClusterManagerClient
	iamPolicyClient saIAMPolicyGetter
	projectID       string
}

func newGKESubCollector(
	client *container.ClusterManagerClient,
	iamPolicyClient saIAMPolicyGetter,
	projectID string,
) *gkeSubCollector {
	return &gkeSubCollector{
		client:          client,
		iamPolicyClient: iamPolicyClient,
		projectID:       projectID,
	}
}

func (c *gkeSubCollector) Name() string { return "gcp-gke" }

func (c *gkeSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	// List clusters across all zones/regions using location "-".
	resp, err := c.client.ListClusters(ctx, &containerpb.ListClustersRequest{
		Parent: fmt.Sprintf("projects/%s/locations/-", c.projectID),
	})
	if err != nil {
		return result, err
	}

	saEmailSet := map[string]struct{}{}

	for _, cluster := range resp.GetClusters() {
		selfLink := cluster.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(cluster)
		if err != nil {
			continue
		}

		spec := cloud.ResourceSpec{
			ID:           selfLink,
			Name:         cluster.GetName(),
			ResourceType: "gcp:container:cluster",
			Region:       cluster.GetLocation(),
			Content:      content,
			Metadata: map[string]string{
				"currentMasterVersion": cluster.GetCurrentMasterVersion(),
				"currentNodeCount":     intStr(cluster.GetCurrentNodeCount()),
				"status":               cluster.GetStatus().String(),
			},
		}
		result.Resources = append(result.Resources, spec)

		result.Edges = append(result.Edges, c.clusterEdges(selfLink, cluster)...)

		// Collect unique SA emails from node pools for WI edge discovery.
		for _, pool := range cluster.GetNodePools() {
			if cfg := pool.GetConfig(); cfg != nil {
				if sa := cfg.GetServiceAccount(); sa != "" {
					saEmailSet[sa] = struct{}{}
				}
			}
		}

		// Cascade target for k8s collector using GKE kubecontext format.
		kubecontext := gkeKubecontext(c.projectID, cluster.GetLocation(), cluster.GetName())
		result.Targets = append(result.Targets, cloud.CollectTarget{
			Collector: "k8s",
			ID:        kubecontext,
		})
	}

	// Discover workload identity edges by querying IAM policies on node pool SAs.
	if c.iamPolicyClient != nil && len(saEmailSet) > 0 {
		emails := make([]string, 0, len(saEmailSet))
		for email := range saEmailSet {
			emails = append(emails, email)
		}
		wiEdges := gkeWorkloadIdentityEdges(ctx, c.iamPolicyClient, c.projectID, emails)
		result.Edges = append(result.Edges, wiEdges...)
	}

	return result, nil
}

// clusterEdges extracts network, subnet, encryption, and service account edges
// for a single GKE cluster.
func (c *gkeSubCollector) clusterEdges(selfLink string, cluster *containerpb.Cluster) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Cluster -> VPC network.
	if network := cluster.GetNetwork(); network != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     selfLink,
			TargetID:     computeSelfLink(c.projectID, "networks", network),
			Relationship: kgtypes.EdgeUsesNetwork,
		})
	}

	// Cluster -> subnet.
	if subnet := cluster.GetSubnetwork(); subnet != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     selfLink,
			TargetID:     computeRegionalSelfLink(c.projectID, cluster.GetLocation(), "subnetworks", subnet),
			Relationship: kgtypes.EdgeUsesSubnet,
		})
	}

	// Cluster → CMEK encryption key (database encryption).
	if dbEnc := cluster.GetDatabaseEncryption(); dbEnc != nil {
		if dbEnc.GetState() == containerpb.DatabaseEncryption_ENCRYPTED && dbEnc.GetKeyName() != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     dbEnc.GetKeyName(),
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}
	}

	// Node pool -> service account edges.
	for _, pool := range cluster.GetNodePools() {
		if cfg := pool.GetConfig(); cfg != nil {
			if sa := cfg.GetServiceAccount(); sa != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     selfLink,
					TargetID:     saResourceName(c.projectID, sa),
					Relationship: kgtypes.EdgeUsesSA,
				})
			}
		}
	}

	return edges
}

// gkeKubecontext builds the standard GKE kubeconfig context name.
// Format: gke_{project}_{zone}_{cluster}
func gkeKubecontext(project, location, cluster string) string {
	return fmt.Sprintf("gke_%s_%s_%s", project, location, cluster)
}

// computeSelfLink builds a global compute self-link URL.
func computeSelfLink(project, resourceType, name string) string {
	// If name is already a full URL, return as-is.
	if strings.HasPrefix(name, "https://") || strings.HasPrefix(name, "projects/") {
		return name
	}
	return fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/%s/global/%s/%s",
		project, resourceType, name)
}

// computeRegionalSelfLink builds a regional compute self-link URL.
func computeRegionalSelfLink(project, region, resourceType, name string) string {
	if strings.HasPrefix(name, "https://") || strings.HasPrefix(name, "projects/") {
		return name
	}
	// GKE location can be a zone (us-central1-a) or region (us-central1).
	// Subnets are regional, so extract the region portion.
	regionPart := extractRegionFromLocation(region)
	return fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/%s/regions/%s/%s/%s",
		project, regionPart, resourceType, name)
}

// extractRegionFromLocation extracts the region from a GCP location.
// If it's already a region (e.g. "us-central1"), returns as-is.
// If it's a zone (e.g. "us-central1-a"), strips the zone suffix.
func extractRegionFromLocation(location string) string {
	parts := strings.Split(location, "-")
	if len(parts) >= 3 {
		// Could be region (us-central1) or zone (us-central1-a).
		// Regions have exactly 2 dashes, zones have 3+.
		// We check if the last part is a single letter (zone indicator).
		last := parts[len(parts)-1]
		if len(last) == 1 && last[0] >= 'a' && last[0] <= 'z' {
			return strings.Join(parts[:len(parts)-1], "-")
		}
	}
	return location
}
