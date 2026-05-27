// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"strconv"

	redis "cloud.google.com/go/redis/apiv1"
	redispb "cloud.google.com/go/redis/apiv1/redispb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// memorystoreSubCollector collects Memorystore (Redis) instances.
type memorystoreSubCollector struct {
	client    *redis.CloudRedisClient
	projectID string
}

func newMemorystoreSubCollector(client *redis.CloudRedisClient, projectID string) *memorystoreSubCollector {
	return &memorystoreSubCollector{client: client, projectID: projectID}
}

func (c *memorystoreSubCollector) Name() string { return "gcp-memorystore" }

func (c *memorystoreSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.ListInstances(ctx, &redispb.ListInstancesRequest{
		Parent: "projects/" + c.projectID + "/locations/-",
	})

	for {
		inst, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		name := inst.GetName()
		if name == "" {
			continue
		}

		content, err := json.Marshal(inst)
		if err != nil {
			continue
		}

		spec := cloud.ResourceSpec{
			ID:           name,
			Name:         extractLast(name),
			ResourceType: "gcp:redis:instance",
			Region:       extractLocationFromName(name),
			Content:      content,
			Metadata: map[string]string{
				"tier":             inst.GetTier().String(),
				"memorySizeGb":     strconv.FormatInt(int64(inst.GetMemorySizeGb()), 10),
				"redisVersion":     inst.GetRedisVersion(),
				"state":            inst.GetState().String(),
				"readReplicasMode": inst.GetReadReplicasMode().String(),
			},
		}
		result.Resources = append(result.Resources, spec)

		// Redis instance -> authorized VPC network.
		if network := inst.GetAuthorizedNetwork(); network != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     name,
				TargetID:     network,
				Relationship: kgtypes.EdgeUsesNetwork,
			})
		}

		// ENCRYPTS_WITH edge when CMEK is configured.
		if cmek := inst.GetCustomerManagedKey(); cmek != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     name,
				TargetID:     cmek,
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}
	}

	return result, nil
}
