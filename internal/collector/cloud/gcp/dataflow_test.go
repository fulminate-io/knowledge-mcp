// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	dataflow "google.golang.org/api/dataflow/v1b3"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestDataflowSubCollector_Name(t *testing.T) {
	c := &dataflowSubCollector{}
	assert.Equal(t, "gcp-dataflow", c.Name())
}

func TestDataflowJobResourceID(t *testing.T) {
	job := &dataflow.Job{
		Id:       "2026-04-11_abc",
		Location: "us-central1",
	}
	assert.Equal(t,
		"projects/p/locations/us-central1/jobs/2026-04-11_abc",
		dataflowJobResourceID("p", job))
}

func TestDataflowJobResourceID_NoLocation(t *testing.T) {
	job := &dataflow.Job{Id: "xyz"}
	assert.Equal(t, "projects/p/locations/-/jobs/xyz", dataflowJobResourceID("p", job))
}

func TestDataflowJobMetadata(t *testing.T) {
	job := &dataflow.Job{
		CurrentState: "JOB_STATE_RUNNING",
		Type:         "JOB_TYPE_STREAMING",
		CreateTime:   "2026-04-11T00:00:00Z",
		JobMetadata: &dataflow.JobMetadata{
			SdkVersion: &dataflow.SdkVersion{Version: "2.50.0"},
		},
	}
	meta := dataflowJobMetadata(job)
	assert.Equal(t, "JOB_STATE_RUNNING", meta["currentState"])
	assert.Equal(t, "JOB_TYPE_STREAMING", meta["type"])
	assert.Equal(t, "2026-04-11T00:00:00Z", meta["createTime"])
	assert.Equal(t, "2.50.0", meta["sdk"])
}

func TestDataflowJobEdges_ServiceAccountAndSubnet(t *testing.T) {
	job := &dataflow.Job{
		Environment: &dataflow.Environment{
			ServiceAccountEmail: "dataflow@p.iam.gserviceaccount.com",
			WorkerPools: []*dataflow.WorkerPool{
				{Subnetwork: "regions/us-central1/subnetworks/my-subnet"},
			},
		},
	}
	edges := dataflowJobEdges("p", "job-1", job)

	var sawSA, sawSubnet bool
	for _, e := range edges {
		if e.Relationship == kgtypes.EdgeUsesSA {
			sawSA = true
			assert.Equal(t, "projects/p/serviceAccounts/dataflow@p.iam.gserviceaccount.com", e.TargetID)
		}
		if e.Relationship == kgtypes.EdgeUsesSubnet {
			sawSubnet = true
			assert.Equal(t, "regions/us-central1/subnetworks/my-subnet", e.TargetID)
		}
	}
	assert.True(t, sawSA, "expected USES_SA edge")
	assert.True(t, sawSubnet, "expected USES_SUBNET edge")
}

func TestDataflowJobEdges_EmptyEnvironment(t *testing.T) {
	job := &dataflow.Job{}
	assert.Empty(t, dataflowJobEdges("p", "job-1", job))
}

func TestDataflowSinksToEdges_BigQueryPubsubFile(t *testing.T) {
	job := &dataflow.Job{
		JobMetadata: &dataflow.JobMetadata{
			BigqueryDetails: []*dataflow.BigQueryIODetails{
				{ProjectId: "p", Dataset: "ds", Table: "t"},
			},
			PubsubDetails: []*dataflow.PubSubIODetails{
				{Topic: "projects/p/topics/t1"},
			},
			FileDetails: []*dataflow.FileIODetails{
				{FilePattern: "gs://bucket/prefix/*"},
			},
		},
	}
	edges := dataflowSinksToEdges("p", "job-1", job)
	assert.Len(t, edges, 3)

	targets := make(map[string]bool)
	for _, e := range edges {
		assert.Equal(t, kgtypes.EdgeSinksTo, e.Relationship)
		assert.Equal(t, "io_details", e.Metadata["inference"])
		targets[e.TargetID] = true
	}
	assert.True(t, targets["projects/p/datasets/ds/tables/t"])
	assert.True(t, targets["projects/p/topics/t1"])
	assert.True(t, targets["gs://bucket/prefix/*"])
}

func TestDataflowSinksToEdges_NoJobMetadata(t *testing.T) {
	assert.Empty(t, dataflowSinksToEdges("p", "job-1", &dataflow.Job{}))
}
