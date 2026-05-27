// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	cloudtasks "google.golang.org/api/cloudtasks/v2"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestTasksSubCollector_Name(t *testing.T) {
	c := &tasksSubCollector{}
	assert.Equal(t, "gcp-cloud-tasks", c.Name())
}

func TestTasksQueueEdges_WithHTTPTarget(t *testing.T) {
	q := &cloudtasks.Queue{
		Name: "projects/p/locations/us-central1/queues/my-queue",
		HttpTarget: &cloudtasks.HttpTarget{
			UriOverride: &cloudtasks.UriOverride{
				Host: "my-service.example.com",
			},
		},
	}

	edges := tasksQueueEdges(q)
	assert.Len(t, edges, 1)
	assert.Equal(t, q.Name, edges[0].SourceID)
	assert.Equal(t, "my-service.example.com", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeRoutesTo, edges[0].Relationship)
}

func TestTasksQueueEdges_NilHTTPTarget(t *testing.T) {
	q := &cloudtasks.Queue{
		Name: "projects/p/locations/us-central1/queues/no-target",
	}

	edges := tasksQueueEdges(q)
	assert.Nil(t, edges)
}

func TestTasksQueueEdges_NilUriOverride(t *testing.T) {
	q := &cloudtasks.Queue{
		Name:       "projects/p/locations/us-central1/queues/no-uri",
		HttpTarget: &cloudtasks.HttpTarget{},
	}

	edges := tasksQueueEdges(q)
	assert.Nil(t, edges)
}

func TestTasksQueueEdges_EmptyHost(t *testing.T) {
	q := &cloudtasks.Queue{
		Name: "projects/p/locations/us-central1/queues/empty-host",
		HttpTarget: &cloudtasks.HttpTarget{
			UriOverride: &cloudtasks.UriOverride{
				Host: "",
			},
		},
	}

	edges := tasksQueueEdges(q)
	assert.Nil(t, edges)
}
