// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"

	eventarc "cloud.google.com/go/eventarc/apiv1"
	eventarcpb "cloud.google.com/go/eventarc/apiv1/eventarcpb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// eventarcSubCollector collects Eventarc triggers.
// Handles both Cloud Run and Cloud Functions destinations.
type eventarcSubCollector struct {
	client    *eventarc.Client
	projectID string
}

func newEventarcSubCollector(client *eventarc.Client, projectID string) *eventarcSubCollector {
	return &eventarcSubCollector{client: client, projectID: projectID}
}

func (c *eventarcSubCollector) Name() string { return "gcp-eventarc-triggers" }

func (c *eventarcSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.ListTriggers(ctx, &eventarcpb.ListTriggersRequest{
		Parent: "projects/" + c.projectID + "/locations/-",
	})

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		trigger, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		name := trigger.GetName()
		if name == "" {
			continue
		}

		spec, edges := buildEventarcTriggerNode(c.projectID, name, trigger)
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, edges...)
	}

	return result, nil
}

// buildEventarcTriggerNode creates the resource spec and edges for an Eventarc trigger.
func buildEventarcTriggerNode(
	projectID, name string, trigger *eventarcpb.Trigger,
) (cloud.ResourceSpec, []cloud.EdgeSpec) {
	content, _ := json.Marshal(map[string]any{ //nolint:errchkjson // best-effort content envelope
		"name":          name,
		"event_filters": eventarcFilterDigest(trigger.GetEventFilters()),
	})

	spec := cloud.ResourceSpec{
		ID:           name,
		Name:         extractLast(name),
		ResourceType: "gcp:eventarc:trigger",
		Region:       extractLocationFromName(name),
		Content:      content,
		Metadata: map[string]string{
			"channel": trigger.GetChannel(),
		},
	}

	edges := eventarcTriggerEdges(projectID, name, trigger)
	return spec, edges
}

// eventarcFilterDigest renders the event filters as a list of {attribute,value,operator}
// tuples for the content envelope. Replaces the prior count-only placeholder so
// downstream graph readers can inspect the actual filter set without a re-collect.
func eventarcFilterDigest(filters []*eventarcpb.EventFilter) []map[string]string {
	if len(filters) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(filters))
	for _, f := range filters {
		entry := map[string]string{
			"attribute": f.GetAttribute(),
			"value":     f.GetValue(),
		}
		if op := f.GetOperator(); op != "" {
			entry["operator"] = op
		}
		out = append(out, entry)
	}
	return out
}

// eventarcTriggerEdges extracts edges from an Eventarc trigger:
// TRIGGERS edges to Cloud Run / Cloud Functions destinations,
// SUBSCRIBES_TO edge to the transport Pub/Sub topic, USES_SA edge for
// the trigger's invocation service account, and TRIGGERS edges from
// upstream event sources (GCS bucket, Firestore database, etc.).
func eventarcTriggerEdges(projectID, triggerName string, trigger *eventarcpb.Trigger) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Destination edges (TRIGGERS).
	edges = append(edges, eventarcDestinationEdges(projectID, triggerName, trigger.GetDestination())...)

	// Transport topic edge (SUBSCRIBES_TO).
	if transport := trigger.GetTransport(); transport != nil {
		if ps := transport.GetPubsub(); ps != nil {
			if topic := ps.GetTopic(); topic != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     triggerName,
					TargetID:     topic,
					Relationship: kgtypes.EdgeSubscribesTo,
				})
			}
		}
	}

	// Trigger invocation service account (USES_SA). Mirrors the OIDC-token
	// pattern in scheduler.go — Eventarc lifts the SA to the trigger
	// top-level rather than nesting it under destination.
	if sa := trigger.GetServiceAccount(); sa != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     triggerName,
			TargetID:     saResourceName(projectID, sa),
			Relationship: kgtypes.EdgeUsesSA,
		})
	}

	// Upstream event-source edges (source → trigger via TRIGGERS).
	edges = append(edges, eventarcSourceEdges(projectID, triggerName, trigger.GetEventFilters())...)

	return edges
}

// eventarcSourceEdges resolves filter attributes that name a specific upstream
// resource (GCS bucket, Firestore database, workflow) and emits a TRIGGERS
// edge from the source resource to the trigger. Filters without a resolvable
// resource (pure type filters, audit-log serviceName) are skipped — only edges
// whose source ID matches a real graph node should be emitted.
func eventarcSourceEdges(
	projectID, triggerName string, filters []*eventarcpb.EventFilter,
) []cloud.EdgeSpec {
	if len(filters) == 0 {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, f := range filters {
		value := f.GetValue()
		if value == "" {
			continue
		}
		var sourceID string
		switch f.GetAttribute() {
		case "bucket":
			// GCS bucket — globally unique by name. Matches storage.go's
			// gs://<bucket> canonical ID format.
			sourceID = "gs://" + value
		case "database":
			// Firestore database — project-scoped.
			sourceID = fmt.Sprintf("projects/%s/databases/%s", projectID, value)
		}
		if sourceID == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     sourceID,
			TargetID:     triggerName,
			Relationship: kgtypes.EdgeTriggers,
			Metadata:     map[string]string{"attribute": f.GetAttribute()},
		})
	}
	return edges
}

// eventarcDestinationEdges builds TRIGGERS edges for an Eventarc destination.
// Handles Cloud Run services, Cloud Functions, and Workflows.
func eventarcDestinationEdges(projectID, triggerName string, dest *eventarcpb.Destination) []cloud.EdgeSpec {
	if dest == nil {
		return nil
	}

	var edges []cloud.EdgeSpec

	// Cloud Run service destination.
	if cr := dest.GetCloudRun(); cr != nil {
		if svc := cr.GetService(); svc != "" {
			// Build the full Cloud Run service resource name.
			region := cr.GetRegion()
			target := fmt.Sprintf("projects/%s/locations/%s/services/%s", projectID, region, svc)
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     triggerName,
				TargetID:     target,
				Relationship: kgtypes.EdgeTriggers,
			})
		}
	}

	// Cloud Function destination (v2, backed by Cloud Run).
	if fn := dest.GetCloudFunction(); fn != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     triggerName,
			TargetID:     fn,
			Relationship: kgtypes.EdgeTriggers,
		})
	}

	// Workflow destination.
	if wf := dest.GetWorkflow(); wf != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     triggerName,
			TargetID:     wf,
			Relationship: kgtypes.EdgeTriggers,
		})
	}

	return edges
}
