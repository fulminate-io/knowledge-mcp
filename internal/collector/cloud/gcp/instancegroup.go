// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// instanceGroupsPairIter is the minimal iterator surface Collect needs.
// *compute.InstanceGroupsScopedListPairIterator satisfies it directly.
// Tests substitute a fake to drive resilience paths.
type instanceGroupsPairIter interface {
	Next() (compute.InstanceGroupsScopedListPair, error)
}

// instanceGroupsAggregator wraps the single SDK call Collect makes. See
// instancesAggregator on compute.go for the rationale; same pattern.
type instanceGroupsAggregator interface {
	AggregatedList(ctx context.Context, projectID string) instanceGroupsPairIter
}

// instanceGroupsClientAggregator is the production adapter.
type instanceGroupsClientAggregator struct {
	client *compute.InstanceGroupsClient
}

func (a instanceGroupsClientAggregator) AggregatedList(
	ctx context.Context, projectID string,
) instanceGroupsPairIter {
	return a.client.AggregatedList(ctx, &computepb.AggregatedListInstanceGroupsRequest{
		Project: projectID,
	})
}

type instanceGroupSubCollector struct {
	client     *compute.InstanceGroupsClient
	aggregator instanceGroupsAggregator
	projectID  string
}

func newInstanceGroupSubCollector(client *compute.InstanceGroupsClient, projectID string) *instanceGroupSubCollector {
	return &instanceGroupSubCollector{
		client:     client,
		aggregator: instanceGroupsClientAggregator{client: client},
		projectID:  projectID,
	}
}

func (c *instanceGroupSubCollector) Name() string { return "gcp-instance-groups" }

func (c *instanceGroupSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.aggregator.AggregatedList(ctx, c.projectID)

	yielded := 0
	for {
		pair, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			switch classifyAggregatedIterErr(err, yielded) {
			case aggregatedIterEmpty:
				slog.Debug("gcp-instance-groups: project-level permission denied, returning empty",
					"project", c.projectID, "err", err)
				return cloud.SubCollectorResult{}, nil
			case aggregatedIterSkipZone:
				slog.Debug("gcp-instance-groups: zone permission denied, stopping iteration",
					"project", c.projectID, "err", err)
				return result, nil
			default:
				return result, fmt.Errorf("instance groups: aggregated list: %w", err)
			}
		}
		yielded++

		if isZoneUnreachableWarning(pair.Value.GetWarning()) {
			slog.Debug("gcp-instance-groups: zone unreachable",
				"project", c.projectID, "zone", pair.Key,
				"message", pair.Value.GetWarning().GetMessage())
			continue
		}

		c.appendZone(ctx, &result, pair.Value.GetInstanceGroups())
	}

	return result, nil
}

// appendZone extracts ResourceSpecs and CONTAINS EdgeSpecs from a single
// zone's instance groups and appends them to result in place. Kept as a
// method because it calls listMembers, which needs the subcollector's
// client and projectID.
func (c *instanceGroupSubCollector) appendZone(
	ctx context.Context, result *cloud.SubCollectorResult,
	groups []*computepb.InstanceGroup,
) {
	for _, ig := range groups {
		selfLink := ig.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(ig)
		if err != nil {
			continue
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           selfLink,
			Name:         ig.GetName(),
			ResourceType: "gcp:compute:instanceGroup",
			Region:       ig.GetZone(),
			Content:      content,
		})

		// Instance group → member instances (CONTAINS)
		memberEdges := c.listMembers(ctx, ig.GetName(), ig.GetZone(), selfLink)
		result.Edges = append(result.Edges, memberEdges...)
	}
}

// listMembers returns CONTAINS edges from the instance group to its member
// instances. Fails open on error.
func (c *instanceGroupSubCollector) listMembers(
	ctx context.Context, igName, zone, igSelfLink string,
) []cloud.EdgeSpec {
	// Extract zone name from full URL (e.g. ".../zones/us-central1-a").
	zoneName := lastSegment(zone)
	if zoneName == "" {
		return nil
	}

	var edges []cloud.EdgeSpec
	it := c.client.ListInstances(ctx, &computepb.ListInstancesInstanceGroupsRequest{
		Project:       c.projectID,
		Zone:          zoneName,
		InstanceGroup: igName,
		InstanceGroupsListInstancesRequestResource: &computepb.InstanceGroupsListInstancesRequest{
			InstanceState: new("ALL"),
		},
	})
	for {
		inst, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			slog.Debug("instance-groups: list instances", "ig", igName, "err", err)
			return edges
		}
		if instLink := inst.GetInstance(); instLink != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     igSelfLink,
				TargetID:     instLink,
				Relationship: kgtypes.EdgeContains,
			})
		}
	}
	return edges
}
