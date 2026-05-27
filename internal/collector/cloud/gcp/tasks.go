// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"

	cloudtasks "google.golang.org/api/cloudtasks/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// tasksSubCollector collects Cloud Tasks queues across all locations.
// Uses the REST-based google.golang.org/api (same pattern as dns, sqladmin).
type tasksSubCollector struct {
	service   *cloudtasks.Service
	projectID string
}

func newTasksSubCollector(service *cloudtasks.Service, projectID string) *tasksSubCollector {
	return &tasksSubCollector{service: service, projectID: projectID}
}

func (c *tasksSubCollector) Name() string { return "gcp-cloud-tasks" }

func (c *tasksSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	locations, err := c.listLocations(ctx)
	if err != nil {
		return result, err
	}

	for _, loc := range locations {
		parent := fmt.Sprintf("projects/%s/locations/%s", c.projectID, loc)
		if err := c.collectQueues(ctx, parent, &result); err != nil {
			return result, err
		}
	}

	return result, nil
}

// listLocations returns all Cloud Tasks location IDs for the project.
func (c *tasksSubCollector) listLocations(ctx context.Context) ([]string, error) {
	var locations []string

	pageToken := ""
	for {
		call := c.service.Projects.Locations.List("projects/" + c.projectID).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("cloud tasks: list locations: %w", err)
		}

		for _, loc := range resp.Locations {
			if loc.LocationId != "" {
				locations = append(locations, loc.LocationId)
			}
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return locations, nil
}

// collectQueues lists queues in a single location and appends to result.
func (c *tasksSubCollector) collectQueues(
	ctx context.Context, parent string, result *cloud.SubCollectorResult,
) error {
	pageToken := ""
	for {
		call := c.service.Projects.Locations.Queues.List(parent).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return fmt.Errorf("cloud tasks: list queues in %s: %w", parent, err)
		}

		for _, q := range resp.Queues {
			if q.Name == "" {
				continue
			}

			content, err := json.Marshal(q)
			if err != nil {
				continue
			}

			spec := cloud.ResourceSpec{
				ID:           q.Name,
				Name:         extractLast(q.Name),
				ResourceType: "gcp:cloudtasks:queue",
				Region:       extractLocationFromName(q.Name),
				Content:      content,
				Metadata: map[string]string{
					"state": q.State,
				},
			}
			if q.RateLimits != nil {
				spec.Metadata["maxDispatchesPerSecond"] = fmt.Sprintf("%.2f",
					q.RateLimits.MaxDispatchesPerSecond)
			}
			result.Resources = append(result.Resources, spec)

			result.Edges = append(result.Edges, tasksQueueEdges(q)...)
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return nil
}

// tasksQueueEdges extracts ROUTES_TO edges from a queue's HTTP target.
func tasksQueueEdges(q *cloudtasks.Queue) []cloud.EdgeSpec {
	if q.HttpTarget == nil {
		return nil
	}

	uri := ""
	if q.HttpTarget.UriOverride != nil {
		uri = q.HttpTarget.UriOverride.Host
	}
	if uri == "" {
		return nil
	}

	return []cloud.EdgeSpec{{
		SourceID:     q.Name,
		TargetID:     uri,
		Relationship: kgtypes.EdgeRoutesTo,
	}}
}
