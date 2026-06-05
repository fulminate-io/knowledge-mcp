// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	monitoringpb "cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// alertPolicySubCollector collects Cloud Monitoring alert policies.
// MONITORS edges are emitted only when conditions reference specific resources
// (not generic resource types).
type alertPolicySubCollector struct {
	client    *monitoring.AlertPolicyClient
	projectID string
}

func newAlertPolicySubCollector(client *monitoring.AlertPolicyClient, projectID string) *alertPolicySubCollector {
	return &alertPolicySubCollector{client: client, projectID: projectID}
}

func (c *alertPolicySubCollector) Name() string { return "gcp-alert-policies" }

func (c *alertPolicySubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult
	seenProxies := map[string]bool{}

	it := c.client.ListAlertPolicies(ctx, &monitoringpb.ListAlertPoliciesRequest{
		Name: "projects/" + c.projectID,
	})

	for {
		policy, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		name := policy.GetName()
		if name == "" {
			continue
		}

		spec, edges, proxies := buildAlertPolicyNode(c.projectID, name, policy, seenProxies)
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, edges...)
		result.Resources = append(result.Resources, proxies...)
	}

	return result, nil
}

// buildAlertPolicyNode creates the resource spec, edges, and proxy nodes
// for an alert policy. Proxy nodes are emitted for uncollected
// notification channels.
func buildAlertPolicyNode(
	projectID, name string, policy *monitoringpb.AlertPolicy,
	seenProxies map[string]bool,
) (cloud.ResourceSpec, []cloud.EdgeSpec, []cloud.ResourceSpec) {
	meta := map[string]string{
		"display_name": policy.GetDisplayName(),
		"severity":     policy.GetSeverity().String(),
	}
	if enabled := policy.GetEnabled(); enabled != nil {
		meta["enabled"] = fmt.Sprintf("%v", enabled.GetValue())
	}

	content, _ := json.Marshal(map[string]any{ //nolint:errchkjson // best-effort content envelope
		"name":         name,
		"display_name": policy.GetDisplayName(),
		"conditions":   len(policy.GetConditions()),
	})

	spec := cloud.ResourceSpec{
		ID:           name,
		Name:         extractLast(name),
		ResourceType: "gcp:monitoring:alertPolicy",
		Content:      content,
		Metadata:     meta,
	}

	edges := alertPolicyEdges(projectID, name, policy)

	// Proxy nodes for notification channels not yet collected.
	var proxies []cloud.ResourceSpec
	for _, ch := range policy.GetNotificationChannels() {
		if ch == "" || seenProxies[ch] {
			continue
		}
		seenProxies[ch] = true
		proxies = append(proxies, cloud.ResourceSpec{
			ID:           ch,
			Name:         extractLast(ch),
			ResourceType: "gcp:monitoring:notificationChannel",
			Metadata: map[string]string{
				"collected":        "false",
				"collected_reason": "no collector registered",
			},
		})
	}

	return spec, edges, proxies
}

// alertPolicyEdges extracts MONITORS edges from alert policy conditions
// and EdgeNotifiesVia edges from notification channels.
func alertPolicyEdges(projectID, policyName string, policy *monitoringpb.AlertPolicy) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	for _, cond := range policy.GetConditions() {
		filter := conditionFilter(cond)
		if filter == "" {
			continue
		}

		targets := resolveMonitoringTargets(projectID, filter)
		for _, target := range targets {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     policyName,
				TargetID:     target,
				Relationship: kgtypes.EdgeMonitors,
			})
		}
	}

	// NOTIFIES_VIA: alert policy → notification channel(s).
	edges = append(edges,
		alertPolicyNotificationEdges(policyName, policy.GetNotificationChannels())...)

	return edges
}

// alertPolicyNotificationEdges emits EdgeNotifiesVia from a policy to
// each referenced notification channel. Notification channels are
// referenced by resource name (e.g. projects/P/notificationChannels/123).
func alertPolicyNotificationEdges(policyName string, channels []string) []cloud.EdgeSpec {
	if len(channels) == 0 {
		return nil
	}
	edges := make([]cloud.EdgeSpec, 0, len(channels))
	for _, ch := range channels {
		if ch == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     policyName,
			TargetID:     ch,
			Relationship: kgtypes.EdgeNotifiesVia,
		})
	}
	return edges
}

// conditionFilter extracts the filter string from a condition, which may be
// a metric threshold or metric absence condition.
func conditionFilter(cond *monitoringpb.AlertPolicy_Condition) string {
	if mt := cond.GetConditionThreshold(); mt != nil {
		return mt.GetFilter()
	}
	if ma := cond.GetConditionAbsent(); ma != nil {
		return ma.GetFilter()
	}
	return ""
}

// filterLabelRE matches resource.labels.<key> = "<value>" in monitoring filter strings.
var filterLabelRE = regexp.MustCompile(`resource\.labels\.(\w+)\s*=\s*"([^"]+)"`)

// filterTypeRE matches resource.type = "<value>".
var filterTypeRE = regexp.MustCompile(`resource\.type\s*=\s*"([^"]+)"`)

// resolveMonitoringTargets parses a monitoring filter and resolves specific
// resource references to GCP resource IDs. Returns nil for generic filters
// that don't reference specific resources.
func resolveMonitoringTargets(projectID, filter string) []string {
	resourceType := ""
	if m := filterTypeRE.FindStringSubmatch(filter); len(m) == 2 {
		resourceType = m[1]
	}
	if resourceType == "" {
		return nil
	}

	labels := make(map[string]string)
	for _, m := range filterLabelRE.FindAllStringSubmatch(filter, -1) {
		labels[m[1]] = m[2]
	}

	return resolveGCPMonitoringResource(projectID, resourceType, labels)
}

// resolveGCPMonitoringResource maps monitoring resource.type + labels to
// specific GCP resource IDs. Only emits results when a specific resource
// is identifiable (not just a resource type).
func resolveGCPMonitoringResource(projectID, resourceType string, labels map[string]string) []string {
	zone := labels["zone"]
	switch resourceType {
	case "gce_instance":
		if id := labels["instance_id"]; id != "" && zone != "" {
			return []string{gceSelfLink(projectID, zone, "instances", id)}
		}
	case "gce_disk":
		if id := labels["device_name"]; id != "" && zone != "" {
			return []string{gceSelfLink(projectID, zone, "disks", id)}
		}
	case "gcs_bucket":
		if name := labels["bucket_name"]; name != "" {
			return []string{"gs://" + name}
		}
	case "cloudsql_database":
		if dbID := labels["database_id"]; dbID != "" {
			// database_id is "project:instance" format.
			parts := strings.SplitN(dbID, ":", 2)
			if len(parts) == 2 {
				return []string{fmt.Sprintf(
					"https://sqladmin.googleapis.com/sql/v1beta4/projects/%s/instances/%s",
					parts[0], parts[1],
				)}
			}
		}
	case "cloud_function":
		if name := labels["function_name"]; name != "" {
			region := labels["region"]
			if region != "" {
				return []string{fmt.Sprintf(
					"projects/%s/locations/%s/functions/%s", projectID, region, name,
				)}
			}
		}
	}

	return nil
}

// gceSelfLink constructs a Compute Engine self-link.
func gceSelfLink(projectID, zone, resource, name string) string {
	return fmt.Sprintf(
		"https://www.googleapis.com/compute/v1/projects/%s/zones/%s/%s/%s",
		projectID, zone, resource, name,
	)
}
