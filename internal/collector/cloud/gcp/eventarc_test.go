// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	eventarcpb "cloud.google.com/go/eventarc/apiv1/eventarcpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestEventarcSubCollector_Name(t *testing.T) {
	c := &eventarcSubCollector{}
	assert.Equal(t, "gcp-eventarc-triggers", c.Name())
}

func TestEventarcTriggerEdges_CloudRun(t *testing.T) {
	trigger := &eventarcpb.Trigger{
		Name: "projects/p/locations/us-central1/triggers/my-trigger",
		Destination: &eventarcpb.Destination{
			Descriptor_: &eventarcpb.Destination_CloudRun{
				CloudRun: &eventarcpb.CloudRun{
					Service: "my-service",
					Region:  "us-central1",
				},
			},
		},
		Transport: &eventarcpb.Transport{
			Intermediary: &eventarcpb.Transport_Pubsub{
				Pubsub: &eventarcpb.Pubsub{
					Topic: "projects/p/topics/my-topic",
				},
			},
		},
	}

	edges := eventarcTriggerEdges("p", trigger.GetName(), trigger)
	require.Len(t, edges, 2) // TRIGGERS + SUBSCRIBES_TO

	// TRIGGERS edge to Cloud Run service.
	assert.Equal(t, kgtypes.EdgeTriggers, edges[0].Relationship)
	assert.Equal(t, trigger.GetName(), edges[0].SourceID)
	assert.Equal(t, "projects/p/locations/us-central1/services/my-service", edges[0].TargetID)

	// SUBSCRIBES_TO edge to transport topic.
	assert.Equal(t, kgtypes.EdgeSubscribesTo, edges[1].Relationship)
	assert.Equal(t, "projects/p/topics/my-topic", edges[1].TargetID)
}

func TestEventarcTriggerEdges_CloudFunction(t *testing.T) {
	fnName := "projects/p/locations/us-central1/functions/my-fn"
	trigger := &eventarcpb.Trigger{
		Name: "projects/p/locations/us-central1/triggers/fn-trigger",
		Destination: &eventarcpb.Destination{
			Descriptor_: &eventarcpb.Destination_CloudFunction{
				CloudFunction: fnName,
			},
		},
	}

	edges := eventarcTriggerEdges("p", trigger.GetName(), trigger)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeTriggers, edges[0].Relationship)
	assert.Equal(t, fnName, edges[0].TargetID)
}

func TestEventarcTriggerEdges_NoDestination(t *testing.T) {
	trigger := &eventarcpb.Trigger{
		Name: "projects/p/locations/us-central1/triggers/empty",
	}

	edges := eventarcTriggerEdges("p", trigger.GetName(), trigger)
	assert.Empty(t, edges)
}

func TestEventarcTriggerEdges_Workflow(t *testing.T) {
	wfName := "projects/p/locations/us-central1/workflows/my-workflow"
	trigger := &eventarcpb.Trigger{
		Name: "projects/p/locations/us-central1/triggers/wf-trigger",
		Destination: &eventarcpb.Destination{
			Descriptor_: &eventarcpb.Destination_Workflow{
				Workflow: wfName,
			},
		},
	}

	edges := eventarcTriggerEdges("p", trigger.GetName(), trigger)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeTriggers, edges[0].Relationship)
	assert.Equal(t, wfName, edges[0].TargetID)
}

func TestEventarcTriggerEdges_ServiceAccount(t *testing.T) {
	// Eventarc lifts the invocation SA to the trigger top-level. Mirrors
	// scheduler.go OIDC token pattern (cb9f10a) — the SA is the identity
	// used to call the destination endpoint.
	trigger := &eventarcpb.Trigger{
		Name:           "projects/p/locations/us-central1/triggers/with-sa",
		ServiceAccount: "invoker@proj.iam.gserviceaccount.com",
	}

	edges := eventarcTriggerEdges("p", trigger.GetName(), trigger)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeUsesSA, edges[0].Relationship)
	assert.Equal(t, trigger.GetName(), edges[0].SourceID)
	assert.Equal(t,
		"projects/p/serviceAccounts/invoker@proj.iam.gserviceaccount.com",
		edges[0].TargetID)
}

func TestEventarcSourceEdges_GCSBucket(t *testing.T) {
	// Canonical Eventarc bucket-event filter: type + bucket attributes.
	// Edge direction is source → trigger ("bucket emits events that fire trigger").
	triggerName := "projects/p/locations/us-central1/triggers/bucket-trigger"
	filters := []*eventarcpb.EventFilter{
		{Attribute: "type", Value: "google.cloud.storage.object.v1.finalized"},
		{Attribute: "bucket", Value: "my-uploads"},
	}

	edges := eventarcSourceEdges("p", triggerName, filters)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeTriggers, edges[0].Relationship)
	assert.Equal(t, "gs://my-uploads", edges[0].SourceID)
	assert.Equal(t, triggerName, edges[0].TargetID)
	assert.Equal(t, "bucket", edges[0].Metadata["attribute"])
}

func TestEventarcSourceEdges_FirestoreDatabase(t *testing.T) {
	triggerName := "projects/p/locations/nam5/triggers/firestore-trigger"
	filters := []*eventarcpb.EventFilter{
		{Attribute: "type", Value: "google.cloud.firestore.document.v1.created"},
		{Attribute: "database", Value: "(default)"},
	}

	edges := eventarcSourceEdges("p", triggerName, filters)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeTriggers, edges[0].Relationship)
	assert.Equal(t, "projects/p/databases/(default)", edges[0].SourceID)
	assert.Equal(t, triggerName, edges[0].TargetID)
}

func TestEventarcSourceEdges_TypeOnlyFilter(t *testing.T) {
	// A pure type filter (e.g. BigQuery audit-log triggers) has no
	// resolvable upstream resource — no edge should be emitted.
	triggerName := "projects/p/locations/us/triggers/bq-trigger"
	filters := []*eventarcpb.EventFilter{
		{Attribute: "type", Value: "google.cloud.bigquery.v2.JobService.InsertJob"},
	}

	edges := eventarcSourceEdges("p", triggerName, filters)
	assert.Empty(t, edges)
}

func TestEventarcSourceEdges_EmptyFilters(t *testing.T) {
	assert.Empty(t, eventarcSourceEdges("p", "trigger", nil))
}

func TestEventarcFilterDigest(t *testing.T) {
	filters := []*eventarcpb.EventFilter{
		{Attribute: "type", Value: "google.cloud.storage.object.v1.finalized"},
		{Attribute: "bucket", Value: "uploads", Operator: "match-path-pattern"},
	}
	out := eventarcFilterDigest(filters)
	require.Len(t, out, 2)
	assert.Equal(t, "type", out[0]["attribute"])
	assert.Equal(t, "google.cloud.storage.object.v1.finalized", out[0]["value"])
	assert.NotContains(t, out[0], "operator")
	assert.Equal(t, "match-path-pattern", out[1]["operator"])
}

func TestEventarcFilterDigest_Empty(t *testing.T) {
	assert.Nil(t, eventarcFilterDigest(nil))
}
