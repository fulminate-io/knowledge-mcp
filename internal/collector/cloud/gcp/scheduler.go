// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"

	cloudscheduler "google.golang.org/api/cloudscheduler/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// schedulerSubCollector collects Cloud Scheduler jobs across all locations.
// Uses the REST-based google.golang.org/api (same pattern as dns, sqladmin).
type schedulerSubCollector struct {
	service   *cloudscheduler.Service
	projectID string
}

func newSchedulerSubCollector(service *cloudscheduler.Service, projectID string) *schedulerSubCollector {
	return &schedulerSubCollector{service: service, projectID: projectID}
}

func (c *schedulerSubCollector) Name() string { return "gcp-cloud-scheduler" }

func (c *schedulerSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	locations, err := c.listLocations(ctx)
	if err != nil {
		return result, err
	}

	for _, loc := range locations {
		parent := fmt.Sprintf("projects/%s/locations/%s", c.projectID, loc)
		if err := c.collectJobs(ctx, parent, &result); err != nil {
			return result, err
		}
	}

	return result, nil
}

// listLocations returns all Cloud Scheduler location IDs for the project.
func (c *schedulerSubCollector) listLocations(ctx context.Context) ([]string, error) {
	var locations []string

	pageToken := ""
	for {
		call := c.service.Projects.Locations.List("projects/" + c.projectID).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("cloud scheduler: list locations: %w", err)
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

// collectJobs lists scheduler jobs in a single location and appends to result.
func (c *schedulerSubCollector) collectJobs(
	ctx context.Context, parent string, result *cloud.SubCollectorResult,
) error {
	pageToken := ""
	for {
		call := c.service.Projects.Locations.Jobs.List(parent).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return fmt.Errorf("cloud scheduler: list jobs in %s: %w", parent, err)
		}

		for _, job := range resp.Jobs {
			if job.Name == "" {
				continue
			}

			content, err := json.Marshal(job)
			if err != nil {
				continue
			}

			spec := cloud.ResourceSpec{
				ID:           job.Name,
				Name:         extractLast(job.Name),
				ResourceType: "gcp:scheduler:job",
				Region:       extractLocationFromName(job.Name),
				Content:      content,
				Metadata: map[string]string{
					"state":    job.State,
					"schedule": job.Schedule,
					"timeZone": job.TimeZone,
				},
			}
			result.Resources = append(result.Resources, spec)

			result.Edges = append(result.Edges, schedulerJobEdges(c.projectID, job)...)
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return nil
}

// schedulerJobEdges extracts TARGETS edges from a scheduler job's targets,
// plus a USES_SA edge to the OIDC/OAuth service account that signs the
// invocation token (if the job's HTTP target carries one). Without the
// USES_SA edge the graph cannot answer "which scheduler jobs run as SA X".
func schedulerJobEdges(projectID string, job *cloudscheduler.Job) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Pub/Sub target -> topic.
	if pt := job.PubsubTarget; pt != nil && pt.TopicName != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     job.Name,
			TargetID:     normalizePubSubTopic(projectID, pt.TopicName),
			Relationship: kgtypes.EdgeTargets,
		})
	}

	// HTTP target -> URI (best-effort) plus the SA used for OIDC/OAuth
	// signing on IAM-protected endpoints (Cloud Run, Cloud Functions,
	// Google APIs). Both token forms are commonly populated; OIDC takes
	// precedence when both are set (mirrors the GCP request flow).
	if ht := job.HttpTarget; ht != nil && ht.Uri != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     job.Name,
			TargetID:     ht.Uri,
			Relationship: kgtypes.EdgeTargets,
		})
	}
	if sa := schedulerHTTPSAEmail(job.HttpTarget); sa != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     job.Name,
			TargetID:     saResourceName(projectID, sa),
			Relationship: kgtypes.EdgeUsesSA,
		})
	}

	return edges
}

// schedulerHTTPSAEmail returns the OIDC service-account email if set, else
// the OAuth one, else "". OIDC wins when both are populated.
func schedulerHTTPSAEmail(ht *cloudscheduler.HttpTarget) string {
	if ht == nil {
		return ""
	}
	if ot := ht.OidcToken; ot != nil && ot.ServiceAccountEmail != "" {
		return ot.ServiceAccountEmail
	}
	if ot := ht.OauthToken; ot != nil {
		return ot.ServiceAccountEmail
	}
	return ""
}
