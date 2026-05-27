// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"

	dataflow "google.golang.org/api/dataflow/v1b3"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// dataflowSubCollector collects active Cloud Dataflow jobs across all regions.
// Uses the REST v1b3 API with filter=ACTIVE so only currently running jobs
// become infrastructure nodes — completed/failed jobs are intentionally
// excluded to avoid stale resources.
type dataflowSubCollector struct {
	service   *dataflow.Service
	projectID string
}

func newDataflowSubCollector(service *dataflow.Service, projectID string) *dataflowSubCollector {
	return &dataflowSubCollector{service: service, projectID: projectID}
}

func (c *dataflowSubCollector) Name() string { return "gcp-dataflow" }

func (c *dataflowSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	pageToken := ""
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		call := c.service.Projects.Jobs.Aggregated(c.projectID).
			Filter("ACTIVE").
			Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return result, fmt.Errorf("dataflow: list jobs: %w", err)
		}

		for _, job := range resp.Jobs {
			if job.Id == "" {
				continue
			}
			c.appendJob(job, &result)
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return result, nil
}

// appendJob converts a dataflow Job into a resource + edges and appends them.
func (c *dataflowSubCollector) appendJob(job *dataflow.Job, result *cloud.SubCollectorResult) {
	jobID := dataflowJobResourceID(c.projectID, job)

	content, _ := json.Marshal(job) //nolint:errchkjson // best-effort content envelope
	result.Resources = append(result.Resources, cloud.ResourceSpec{
		ID:           jobID,
		Name:         job.Name,
		ResourceType: "gcp:dataflow:job",
		Region:       job.Location,
		Content:      content,
		Metadata:     dataflowJobMetadata(job),
	})
	result.Edges = append(result.Edges, dataflowJobEdges(c.projectID, jobID, job)...)
}

// dataflowJobResourceID builds a stable resource ID for a dataflow job using
// the regional resource name form.
func dataflowJobResourceID(projectID string, job *dataflow.Job) string {
	location := job.Location
	if location == "" {
		location = "-"
	}
	return fmt.Sprintf("projects/%s/locations/%s/jobs/%s", projectID, location, job.Id)
}

// dataflowJobMetadata extracts searchable metadata from a job.
func dataflowJobMetadata(job *dataflow.Job) map[string]string {
	meta := map[string]string{
		"currentState": job.CurrentState,
		"type":         job.Type,
	}
	if job.CreateTime != "" {
		meta["createTime"] = job.CreateTime
	}
	if info := job.JobMetadata; info != nil && info.SdkVersion != nil {
		meta["sdk"] = info.SdkVersion.Version
	}
	return meta
}

// dataflowJobEdges builds the USES_SA, USES_SUBNET, and SINKS_TO edges for a
// dataflow job. USES_SA targets the service account resource derived from
// Environment.ServiceAccountEmail. USES_SUBNET targets the subnet string from
// the first worker pool with a Subnetwork set. SINKS_TO is best-effort,
// derived from job.JobMetadata IODetails which describe connectors used by
// the pipeline; the API does not disambiguate read-vs-write direction, so
// these edges carry an "inferred" metadata marker.
func dataflowJobEdges(projectID, jobID string, job *dataflow.Job) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	if env := job.Environment; env != nil {
		if sa := env.ServiceAccountEmail; sa != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     jobID,
				TargetID:     saResourceName(projectID, sa),
				Relationship: kgtypes.EdgeUsesSA,
			})
		}

		for _, wp := range env.WorkerPools {
			if subnet := wp.Subnetwork; subnet != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     jobID,
					TargetID:     subnet,
					Relationship: kgtypes.EdgeUsesSubnet,
				})
				break
			}
		}
	}

	edges = append(edges, dataflowSinksToEdges(projectID, jobID, job)...)
	return edges
}

// dataflowSinksToEdges emits SINKS_TO edges for each connector referenced in
// the job's IODetails. The direction is inferred best-effort and marked in
// edge metadata.
func dataflowSinksToEdges(projectID, jobID string, job *dataflow.Job) []cloud.EdgeSpec {
	md := job.JobMetadata
	if md == nil {
		return nil
	}
	meta := map[string]string{"inference": "io_details"}
	var edges []cloud.EdgeSpec

	for _, bq := range md.BigqueryDetails {
		pid := bq.ProjectId
		if pid == "" {
			pid = projectID
		}
		if bq.Dataset == "" {
			continue
		}
		target := fmt.Sprintf("projects/%s/datasets/%s", pid, bq.Dataset)
		if bq.Table != "" {
			target += "/tables/" + bq.Table
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     jobID,
			TargetID:     target,
			Relationship: kgtypes.EdgeSinksTo,
			Metadata:     meta,
		})
	}

	for _, ps := range md.PubsubDetails {
		if ps.Topic == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     jobID,
			TargetID:     ps.Topic,
			Relationship: kgtypes.EdgeSinksTo,
			Metadata:     meta,
		})
	}

	for _, fd := range md.FileDetails {
		if fd.FilePattern == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     jobID,
			TargetID:     fd.FilePattern,
			Relationship: kgtypes.EdgeSinksTo,
			Metadata:     meta,
		})
	}

	return edges
}
