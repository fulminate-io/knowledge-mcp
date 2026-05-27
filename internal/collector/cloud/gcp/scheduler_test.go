// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	cloudscheduler "google.golang.org/api/cloudscheduler/v1"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestSchedulerSubCollector_Name(t *testing.T) {
	c := &schedulerSubCollector{}
	assert.Equal(t, "gcp-cloud-scheduler", c.Name())
}

func TestSchedulerJobEdges_PubSubTarget(t *testing.T) {
	job := &cloudscheduler.Job{
		Name: "projects/p/locations/us-central1/jobs/pub-job",
		PubsubTarget: &cloudscheduler.PubsubTarget{
			TopicName: "projects/p/topics/my-topic",
		},
	}

	edges := schedulerJobEdges("p", job)
	assert.Len(t, edges, 1)
	assert.Equal(t, job.Name, edges[0].SourceID)
	assert.Equal(t, "projects/p/topics/my-topic", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
}

func TestSchedulerJobEdges_PubSubShortName(t *testing.T) {
	// Short topic name should be normalized to full resource path.
	job := &cloudscheduler.Job{
		Name: "projects/p/locations/us-central1/jobs/short-topic-job",
		PubsubTarget: &cloudscheduler.PubsubTarget{
			TopicName: "my-topic",
		},
	}

	edges := schedulerJobEdges("my-project", job)
	assert.Len(t, edges, 1)
	assert.Equal(t, "projects/my-project/topics/my-topic", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
}

func TestSchedulerJobEdges_HTTPTarget(t *testing.T) {
	job := &cloudscheduler.Job{
		Name: "projects/p/locations/us-central1/jobs/http-job",
		HttpTarget: &cloudscheduler.HttpTarget{
			Uri: "https://my-service.run.app/handler",
		},
	}

	edges := schedulerJobEdges("p", job)
	assert.Len(t, edges, 1)
	assert.Equal(t, job.Name, edges[0].SourceID)
	assert.Equal(t, "https://my-service.run.app/handler", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
}

func TestSchedulerJobEdges_BothTargets(t *testing.T) {
	// A job with both Pub/Sub and HTTP targets produces two edges.
	job := &cloudscheduler.Job{
		Name: "projects/p/locations/us-central1/jobs/dual-job",
		PubsubTarget: &cloudscheduler.PubsubTarget{
			TopicName: "projects/p/topics/events",
		},
		HttpTarget: &cloudscheduler.HttpTarget{
			Uri: "https://backup.example.com/trigger",
		},
	}

	edges := schedulerJobEdges("p", job)
	assert.Len(t, edges, 2)
	assert.Equal(t, "projects/p/topics/events", edges[0].TargetID)
	assert.Equal(t, "https://backup.example.com/trigger", edges[1].TargetID)
}

func TestSchedulerJobEdges_NoTargets(t *testing.T) {
	job := &cloudscheduler.Job{
		Name: "projects/p/locations/us-central1/jobs/empty-job",
	}

	edges := schedulerJobEdges("p", job)
	assert.Empty(t, edges)
}

func TestSchedulerJobEdges_EmptyPubSubTopic(t *testing.T) {
	job := &cloudscheduler.Job{
		Name: "projects/p/locations/us-central1/jobs/no-topic",
		PubsubTarget: &cloudscheduler.PubsubTarget{
			TopicName: "",
		},
	}

	edges := schedulerJobEdges("p", job)
	assert.Empty(t, edges)
}

func TestSchedulerJobEdges_EmptyHTTPUri(t *testing.T) {
	job := &cloudscheduler.Job{
		Name: "projects/p/locations/us-central1/jobs/no-uri",
		HttpTarget: &cloudscheduler.HttpTarget{
			Uri: "",
		},
	}

	edges := schedulerJobEdges("p", job)
	assert.Empty(t, edges)
}
