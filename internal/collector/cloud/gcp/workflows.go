// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"regexp"

	workflows "cloud.google.com/go/workflows/apiv1"
	"cloud.google.com/go/workflows/apiv1/workflowspb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// workflowsSubCollector collects Cloud Workflows across all locations.
type workflowsSubCollector struct {
	client    *workflows.Client
	projectID string
}

func newWorkflowsSubCollector(client *workflows.Client, projectID string) *workflowsSubCollector {
	return &workflowsSubCollector{client: client, projectID: projectID}
}

func (c *workflowsSubCollector) Name() string { return "gcp-workflows" }

func (c *workflowsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	parent := "projects/" + c.projectID + "/locations/-"
	it := c.client.ListWorkflows(ctx, &workflowspb.ListWorkflowsRequest{Parent: parent})
	for {
		wf, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}
		name := wf.GetName()
		if name == "" {
			continue
		}

		content, _ := json.Marshal(wf) //nolint:errchkjson // best-effort content envelope
		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           name,
			Name:         extractLast(name),
			ResourceType: "gcp:workflows:workflow",
			Region:       kmsLocationFromName(name),
			Content:      content,
			Metadata: map[string]string{
				"state":      wf.GetState().String(),
				"revisionId": wf.GetRevisionId(),
			},
		})
		result.Edges = append(result.Edges, workflowEdges(c.projectID, name, wf)...)
	}

	return result, nil
}

// workflowEdges emits USES_SA for the service account binding and TRIGGERS
// edges derived from best-effort parsing of the workflow source for
// googleapis connector calls.
func workflowEdges(projectID, workflowID string, wf *workflowspb.Workflow) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	if sa := wf.GetServiceAccount(); sa != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     workflowID,
			TargetID:     workflowSAResourceName(projectID, sa),
			Relationship: kgtypes.EdgeUsesSA,
		})
	}

	if src := wf.GetSourceContents(); src != "" {
		for _, target := range parseWorkflowTriggers(projectID, src) {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     workflowID,
				TargetID:     target,
				Relationship: kgtypes.EdgeTriggers,
			})
		}
	}

	return edges
}

// workflowConnectorPattern matches googleapis connector calls in a workflow
// definition. Example match: `call: googleapis.cloudfunctions.v2.projects.locations.functions.callFunction`
// captures "cloudfunctions" as the service.
var workflowConnectorPattern = regexp.MustCompile(`googleapis\.([a-zA-Z0-9]+)\.`)

// parseWorkflowTriggers extracts unique service identifiers that this workflow
// calls via googleapis connectors. The returned targets are synthetic
// project-scoped service IDs of the form "projects/P/services/<service>".
// This is intentionally coarse: the exact resource being called (e.g. which
// Cloud Function, which Cloud Run service) cannot be derived without
// interpreting argument expressions, so we expose the fact that the workflow
// depends on a GCP service surface at all and leave fine-grained resolution
// to a later pass.
func parseWorkflowTriggers(projectID, source string) []string {
	matches := workflowConnectorPattern.FindAllStringSubmatch(source, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	targets := make([]string, 0, len(matches))
	for _, m := range matches {
		svc := m[1]
		if svc == "" {
			continue
		}
		if _, ok := seen[svc]; ok {
			continue
		}
		seen[svc] = struct{}{}
		targets = append(targets, "projects/"+projectID+"/services/"+svc)
	}
	return targets
}

// workflowSAResourceName normalizes a service account email to the canonical
// IAM resource name "projects/P/serviceAccounts/<email>".
func workflowSAResourceName(projectID, sa string) string {
	// Already a full resource name.
	if len(sa) > len("projects/") && sa[:len("projects/")] == "projects/" {
		return sa
	}
	return saResourceName(projectID, sa)
}
