// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type ecsCollector struct {
	client    *ecs.Client
	region    string
	accountID string
}

func newECSCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &ecsCollector{
		client:    ecs.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *ecsCollector) Name() string { return "ecs" }

func (c *ecsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
		targets   []cloud.CollectTarget
	)

	// List all ECS clusters.
	clusterPaginator := ecs.NewListClustersPaginator(c.client, &ecs.ListClustersInput{})
	var clusterARNs []string
	for clusterPaginator.HasMorePages() {
		page, err := clusterPaginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("ecs: list clusters: %w", err)
		}
		clusterARNs = append(clusterARNs, page.ClusterArns...)
	}

	if len(clusterARNs) == 0 {
		return cloud.SubCollectorResult{}, nil
	}

	// DescribeClusters accepts up to 100 ARNs at a time.
	for i := 0; i < len(clusterARNs); i += 100 {
		end := min(i+100, len(clusterARNs))
		batch := clusterARNs[i:end]

		desc, err := c.client.DescribeClusters(ctx, &ecs.DescribeClustersInput{
			Clusters: batch,
		})
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("ecs: describe clusters: %w", err)
		}

		for _, cluster := range desc.Clusters {
			content, err := json.Marshal(cluster)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("ecs: marshal cluster: %w", err)
			}

			clusterARN := awssdk.ToString(cluster.ClusterArn)
			clusterName := awssdk.ToString(cluster.ClusterName)

			resources = append(resources, cloud.ResourceSpec{
				ID:           clusterARN,
				Name:         clusterName,
				ResourceType: "ecs-cluster",
				Region:       c.region,
				Content:      content,
				Metadata:     ecsClusterMetadata(cluster),
			})

			// Collect services for this cluster.
			svcResources, svcEdges, svcTargets, err := c.collectServices(ctx, clusterARN)
			if err != nil {
				return cloud.SubCollectorResult{}, err
			}
			resources = append(resources, svcResources...)
			edges = append(edges, svcEdges...)
			targets = append(targets, svcTargets...)
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
		Targets:   targets,
	}, nil
}

func (c *ecsCollector) collectServices(ctx context.Context, clusterARN string) ([]cloud.ResourceSpec, []cloud.EdgeSpec, []cloud.CollectTarget, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
		targets   []cloud.CollectTarget
		seenTasks = make(map[string]struct{})
		seenTgts  = make(map[string]struct{})
	)

	serviceARNs, err := c.listServiceARNs(ctx, clusterARN)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(serviceARNs) == 0 {
		return nil, nil, nil, nil
	}

	// DescribeServices accepts up to 10 ARNs at a time.
	for i := 0; i < len(serviceARNs); i += 10 {
		end := min(i+10, len(serviceARNs))
		desc, err := c.client.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster:  awssdk.String(clusterARN),
			Services: serviceARNs[i:end],
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("ecs: describe services for %s: %w", clusterARN, err)
		}

		for _, svc := range desc.Services {
			res, svcEdges, err := c.buildService(svc)
			if err != nil {
				return nil, nil, nil, err
			}
			resources = append(resources, res)
			edges = append(edges, svcEdges...)

			// Task definition: extract image targets and task role edges.
			taskDefARN := awssdk.ToString(svc.TaskDefinition)
			if taskDefARN == "" {
				continue
			}
			if _, seen := seenTasks[taskDefARN]; seen {
				continue
			}
			seenTasks[taskDefARN] = struct{}{}

			tdEdges, tdTargets, err := c.processTaskDefinition(ctx, awssdk.ToString(svc.ServiceArn), taskDefARN, seenTgts)
			if err != nil {
				return nil, nil, nil, err
			}
			edges = append(edges, tdEdges...)
			targets = append(targets, tdTargets...)
		}
	}

	return resources, edges, targets, nil
}

// listServiceARNs paginates through all service ARNs in a cluster.
func (c *ecsCollector) listServiceARNs(ctx context.Context, clusterARN string) ([]string, error) {
	svcPaginator := ecs.NewListServicesPaginator(c.client, &ecs.ListServicesInput{
		Cluster: awssdk.String(clusterARN),
	})
	var arns []string
	for svcPaginator.HasMorePages() {
		page, err := svcPaginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("ecs: list services for %s: %w", clusterARN, err)
		}
		arns = append(arns, page.ServiceArns...)
	}
	return arns, nil
}

// buildService creates a ResourceSpec and edges for a single ECS service.
func (c *ecsCollector) buildService(svc ecstypes.Service) (cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	content, err := json.Marshal(svc)
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("ecs: marshal service: %w", err)
	}

	svcARN := awssdk.ToString(svc.ServiceArn)
	res := cloud.ResourceSpec{
		ID:           svcARN,
		Name:         awssdk.ToString(svc.ServiceName),
		ResourceType: "ecs-service",
		Region:       c.region,
		Content:      content,
		Metadata:     ecsServiceMetadata(svc),
	}

	var edges []cloud.EdgeSpec
	if svc.RoleArn != nil {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     svcARN,
			TargetID:     awssdk.ToString(svc.RoleArn),
			Relationship: kgtypes.EdgeAssumesRole,
			Metadata:     map[string]string{"role_source": "service_role"},
		})
	}
	edges = append(edges, c.awsvpcEdges(svcARN, svc.NetworkConfiguration)...)

	// ECS service → Target Group (via LoadBalancers config).
	for _, lb := range svc.LoadBalancers {
		if lb.TargetGroupArn == nil {
			continue
		}
		meta := map[string]string{}
		if lb.ContainerName != nil {
			meta["container_name"] = *lb.ContainerName
		}
		if lb.ContainerPort != nil && *lb.ContainerPort != 0 {
			meta["container_port"] = fmt.Sprintf("%d", *lb.ContainerPort)
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     svcARN,
			TargetID:     awssdk.ToString(lb.TargetGroupArn),
			Relationship: kgtypes.EdgeTargets,
			Metadata:     meta,
		})
	}

	return res, edges, nil
}

// ecsClusterMetadata extracts discriminating fields from an ECS cluster.
func ecsClusterMetadata(c ecstypes.Cluster) map[string]string {
	m := make(map[string]string, 1)
	if s := awssdk.ToString(c.Status); s != "" {
		m["status"] = s
	}
	return m
}

// ecsServiceMetadata extracts discriminating fields from an ECS service.
func ecsServiceMetadata(s ecstypes.Service) map[string]string {
	m := make(map[string]string, 3)
	if s.DesiredCount != 0 {
		m["desired_count"] = fmt.Sprintf("%d", s.DesiredCount)
	}
	if lt := string(s.LaunchType); lt != "" {
		m["launch_type"] = lt
	}
	if st := awssdk.ToString(s.Status); st != "" {
		m["status"] = st
	}
	return m
}
