// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	functions "cloud.google.com/go/functions/apiv2"
	functionspb "cloud.google.com/go/functions/apiv2/functionspb"
	iamv1 "cloud.google.com/go/iam/apiv1"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// functionsSubCollector collects Cloud Functions across all locations.
type functionsSubCollector struct {
	client    *functions.FunctionClient
	iamClient *iamv1.IamPolicyClient
	projectID string
}

func newFunctionsSubCollector(
	client *functions.FunctionClient,
	iamClient *iamv1.IamPolicyClient,
	projectID string,
) *functionsSubCollector {
	return &functionsSubCollector{
		client:    client,
		iamClient: iamClient,
		projectID: projectID,
	}
}

func (c *functionsSubCollector) Name() string { return "gcp-cloud-functions" }

func (c *functionsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.ListFunctions(ctx, &functionspb.ListFunctionsRequest{
		Parent: "projects/" + c.projectID + "/locations/-",
	})

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		fn, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		name := fn.GetName()
		if name == "" {
			continue
		}

		content, err := json.Marshal(fn)
		if err != nil {
			continue
		}
		region := extractLocationFromName(name)

		spec := cloud.ResourceSpec{
			ID:           name,
			Name:         extractLast(name),
			ResourceType: "gcp:cloudfunctions:function",
			Region:       region,
			Content:      content,
			Metadata: map[string]string{
				"state":       fn.GetState().String(),
				"environment": fn.GetEnvironment().String(),
				"runtime":     extractRuntime(fn),
			},
		}
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, functionEdges(c.projectID, name, fn)...)

		// Best-effort IAM policy (separate RPC). Captures
		// roles/cloudfunctions.invoker grants — public-exposure signal.
		if c.iamClient != nil {
			if policy, perr := c.iamClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{
				Resource: name,
			}); perr == nil && policy != nil {
				result.Edges = append(result.Edges, functionsIAMGrantsEdges(name, policy)...)
			} else if perr != nil {
				slog.Debug("gcp-cloud-functions: iam policy unavailable",
					"function", name, "error", perr)
			}
		}
	}

	return result, nil
}

// functionEdges extracts edges from a Cloud Function:
// service account, pub/sub trigger, VPC connector, secret env vars, and secret volumes.
func functionEdges(projectID, name string, fn *functionspb.Function) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Service account edge.
	if sc := fn.GetServiceConfig(); sc != nil {
		if sa := sc.GetServiceAccountEmail(); sa != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     name,
				TargetID:     saResourceName(projectID, sa),
				Relationship: kgtypes.EdgeUsesSA,
			})
		}

		// VPC connector edge (Serverless VPC Access).
		if connector := sc.GetVpcConnector(); connector != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     name,
				TargetID:     connector,
				Relationship: kgtypes.EdgeRoutesTo,
			})
		}

		edges = append(edges, functionSecretEdges(projectID, name, sc)...)
	}

	// Event-source edges. Pub/Sub triggers carry the topic on the trigger
	// directly; GCS / Firestore triggers carry the source as an event filter
	// (attribute=bucket / attribute=database) on the event_filters list.
	// All event-source edges share the SUBSCRIBES_TO direction (function
	// listens to upstream resource).
	if trigger := fn.GetEventTrigger(); trigger != nil {
		if topic := trigger.GetPubsubTopic(); topic != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     name,
				TargetID:     normalizePubSubTopic(projectID, topic),
				Relationship: kgtypes.EdgeSubscribesTo,
			})
		}
		edges = append(edges, functionEventFilterEdges(projectID, name, trigger)...)
	}

	return edges
}

// functionEventFilterEdges resolves the EventTrigger.event_filters list into
// SUBSCRIBES_TO edges from the function to the upstream resource the trigger
// fires on. Pub/Sub is handled in the parent function (carried separately on
// the trigger). Filter attributes vary per event type:
//
//   - bucket   → gs://<name> (GCS object events)
//   - database → projects/<P>/databases/<id> (Firestore document events)
//
// Other attributes (audit-log serviceName/methodName, workflow, etc.) produce
// no edge today — they need source-side sentinels which are out of scope.
func functionEventFilterEdges(
	projectID, name string, trigger *functionspb.EventTrigger,
) []cloud.EdgeSpec {
	if trigger == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, f := range trigger.GetEventFilters() {
		value := f.GetValue()
		if value == "" {
			continue
		}
		var target string
		switch f.GetAttribute() {
		case "bucket":
			target = "gs://" + value
		case "database":
			target = "projects/" + projectID + "/databases/" + value
		default:
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     name,
			TargetID:     target,
			Relationship: kgtypes.EdgeSubscribesTo,
			Metadata: map[string]string{
				"eventType": trigger.GetEventType(),
			},
		})
	}
	return edges
}

// functionSecretEdges extracts secret edges from a function's ServiceConfig.
func functionSecretEdges(defaultProjectID, name string, sc *functionspb.ServiceConfig) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	for _, sev := range sc.GetSecretEnvironmentVariables() {
		if secret := sev.GetSecret(); secret != "" {
			pid := sev.GetProjectId()
			if pid == "" {
				pid = defaultProjectID
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     name,
				TargetID:     secretResourceName(pid, secret),
				Relationship: kgtypes.EdgeMountsSecret,
			})
		}
	}

	for _, sv := range sc.GetSecretVolumes() {
		if secret := sv.GetSecret(); secret != "" {
			pid := sv.GetProjectId()
			if pid == "" {
				pid = defaultProjectID
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     name,
				TargetID:     secretResourceName(pid, secret),
				Relationship: kgtypes.EdgeMountsSecret,
			})
		}
	}

	return edges
}

// functionsIAMGrantsEdges turns an iampb.Policy into EdgeGrants edges from
// the Cloud Function to each IAM member. allUsers / allAuthenticatedUsers
// surface here as ordinary member sentinels.
func functionsIAMGrantsEdges(functionName string, policy *iampb.Policy) []cloud.EdgeSpec {
	if policy == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, binding := range policy.GetBindings() {
		role := binding.GetRole()
		members := make([]string, len(binding.GetMembers()))
		copy(members, binding.GetMembers())
		sort.Strings(members)
		for _, member := range members {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     functionName,
				TargetID:     member,
				Relationship: kgtypes.EdgeGrants,
				Metadata:     map[string]string{"role": role},
			})
		}
	}
	return edges
}

// extractRuntime extracts the runtime from a Function's BuildConfig.
func extractRuntime(fn *functionspb.Function) string {
	if bc := fn.GetBuildConfig(); bc != nil {
		return bc.GetRuntime()
	}
	return ""
}

// normalizePubSubTopic ensures a Pub/Sub topic is in full resource name format.
func normalizePubSubTopic(projectID, topic string) string {
	if strings.HasPrefix(topic, "projects/") {
		return topic
	}
	return "projects/" + projectID + "/topics/" + topic
}
