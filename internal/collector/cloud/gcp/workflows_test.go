// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"sort"
	"testing"

	"cloud.google.com/go/workflows/apiv1/workflowspb"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestWorkflowsSubCollector_Name(t *testing.T) {
	c := &workflowsSubCollector{}
	assert.Equal(t, "gcp-workflows", c.Name())
}

func TestParseWorkflowTriggers_Connectors(t *testing.T) {
	source := `
main:
  steps:
    - call_fn:
        call: googleapis.cloudfunctions.v2.projects.locations.functions.callFunction
        args:
          name: projects/p/locations/us-central1/functions/hello
    - publish:
        call: googleapis.pubsub.v1.projects.topics.publish
        args:
          topic: projects/p/topics/t
    - call_fn_again:
        call: googleapis.cloudfunctions.v2.projects.locations.functions.callFunction
`
	targets := parseWorkflowTriggers("my-project", source)
	sort.Strings(targets)
	assert.Equal(t, []string{
		"projects/my-project/services/cloudfunctions",
		"projects/my-project/services/pubsub",
	}, targets)
}

func TestParseWorkflowTriggers_NoConnectors(t *testing.T) {
	source := `
main:
  steps:
    - noop:
        return: hello
`
	assert.Nil(t, parseWorkflowTriggers("my-project", source))
}

func TestWorkflowEdges_ServiceAccount(t *testing.T) {
	wf := &workflowspb.Workflow{
		ServiceAccount: "runner@my-project.iam.gserviceaccount.com",
	}
	edges := workflowEdges("my-project", "projects/my-project/locations/us-central1/workflows/w", wf)
	assert.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeUsesSA, edges[0].Relationship)
	assert.Equal(t, "projects/my-project/serviceAccounts/runner@my-project.iam.gserviceaccount.com",
		edges[0].TargetID)
}

func TestWorkflowEdges_ServiceAccountFullyQualified(t *testing.T) {
	full := "projects/my-project/serviceAccounts/runner@my-project.iam.gserviceaccount.com"
	wf := &workflowspb.Workflow{ServiceAccount: full}
	edges := workflowEdges("my-project", "w", wf)
	assert.Len(t, edges, 1)
	assert.Equal(t, full, edges[0].TargetID)
}

func TestWorkflowEdges_Triggers(t *testing.T) {
	wf := &workflowspb.Workflow{
		SourceCode: &workflowspb.Workflow_SourceContents{
			SourceContents: `
main:
  steps:
    - call_fn:
        call: googleapis.cloudrun.v2.projects.locations.services.get
`,
		},
	}
	edges := workflowEdges("my-project", "projects/my-project/locations/us-central1/workflows/w", wf)
	var triggers []string
	for _, e := range edges {
		if e.Relationship == kgtypes.EdgeTriggers {
			triggers = append(triggers, e.TargetID)
		}
	}
	assert.Equal(t, []string{"projects/my-project/services/cloudrun"}, triggers)
}

func TestWorkflowEdges_NoSource(t *testing.T) {
	wf := &workflowspb.Workflow{}
	assert.Empty(t, workflowEdges("my-project", "w", wf))
}
