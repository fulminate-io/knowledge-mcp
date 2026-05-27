// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	functionspb "cloud.google.com/go/functions/apiv2/functionspb"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestFunctionsSubCollector_Name(t *testing.T) {
	c := &functionsSubCollector{}
	assert.Equal(t, "gcp-cloud-functions", c.Name())
}

func TestFunctionEventFilterEdges_GCSBucket(t *testing.T) {
	const (
		projectID = "p"
		fnName    = "projects/p/locations/us-central1/functions/on-upload"
	)
	trigger := &functionspb.EventTrigger{
		EventType: "google.cloud.storage.object.v1.finalized",
		EventFilters: []*functionspb.EventFilter{
			{Attribute: "bucket", Value: "uploads-bucket"},
		},
	}
	edges := functionEventFilterEdges(projectID, fnName, trigger)
	require.Len(t, edges, 1)
	assert.Equal(t, fnName, edges[0].SourceID)
	assert.Equal(t, "gs://uploads-bucket", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeSubscribesTo, edges[0].Relationship)
	assert.Equal(t, "google.cloud.storage.object.v1.finalized", edges[0].Metadata["eventType"])
}

func TestFunctionEventFilterEdges_FirestoreDatabase(t *testing.T) {
	const (
		projectID = "p"
		fnName    = "projects/p/locations/us-central1/functions/on-write"
	)
	trigger := &functionspb.EventTrigger{
		EventType: "google.cloud.firestore.document.v1.created",
		EventFilters: []*functionspb.EventFilter{
			{Attribute: "database", Value: "(default)"},
		},
	}
	edges := functionEventFilterEdges(projectID, fnName, trigger)
	require.Len(t, edges, 1)
	assert.Equal(t, "projects/p/databases/(default)", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeSubscribesTo, edges[0].Relationship)
}

func TestFunctionEventFilterEdges_UnknownAttributeIgnored(t *testing.T) {
	trigger := &functionspb.EventTrigger{
		EventType: "google.cloud.audit.log.v1.written",
		EventFilters: []*functionspb.EventFilter{
			{Attribute: "serviceName", Value: "storage.googleapis.com"},
			{Attribute: "methodName", Value: "storage.objects.create"},
		},
	}
	edges := functionEventFilterEdges("p", "fn", trigger)
	assert.Empty(t, edges, "audit-log serviceName/methodName produce no edge today")
}

func TestFunctionEventFilterEdges_NilTrigger(t *testing.T) {
	assert.Empty(t, functionEventFilterEdges("p", "fn", nil))
}

// --- EdgeGrants (IAM) ---

func TestFunctionsIAMGrantsEdges_PublicAccess(t *testing.T) {
	// roles/cloudfunctions.invoker → allUsers — public-exposure signal.
	fnName := "projects/p/locations/us-central1/functions/public-fn"
	policy := &iampb.Policy{
		Bindings: []*iampb.Binding{
			{
				Role:    "roles/cloudfunctions.invoker",
				Members: []string{"allUsers"},
			},
		},
	}
	edges := functionsIAMGrantsEdges(fnName, policy)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeGrants, edges[0].Relationship)
	assert.Equal(t, fnName, edges[0].SourceID)
	assert.Equal(t, "allUsers", edges[0].TargetID)
	assert.Equal(t, "roles/cloudfunctions.invoker", edges[0].Metadata["role"])
}

func TestFunctionsIAMGrantsEdges_MultipleBindings(t *testing.T) {
	fnName := "projects/p/locations/us-central1/functions/fn"
	policy := &iampb.Policy{
		Bindings: []*iampb.Binding{
			{
				Role:    "roles/cloudfunctions.invoker",
				Members: []string{"serviceAccount:invoker@proj.iam.gserviceaccount.com"},
			},
			{
				Role:    "roles/cloudfunctions.developer",
				Members: []string{"user:dev@example.com"},
			},
		},
	}
	edges := functionsIAMGrantsEdges(fnName, policy)
	require.Len(t, edges, 2)
	for _, e := range edges {
		assert.Equal(t, kgtypes.EdgeGrants, e.Relationship)
		assert.Equal(t, fnName, e.SourceID)
	}
}

func TestFunctionsIAMGrantsEdges_NilPolicy(t *testing.T) {
	assert.Empty(t, functionsIAMGrantsEdges("fn", nil))
}
