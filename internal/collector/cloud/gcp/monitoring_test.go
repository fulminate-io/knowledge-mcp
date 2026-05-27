// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	monitoringpb "cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestAlertPolicySubCollector_Name(t *testing.T) {
	c := &alertPolicySubCollector{}
	assert.Equal(t, "gcp-alert-policies", c.Name())
}

func TestResolveMonitoringTargets_GCEInstance(t *testing.T) {
	filter := `resource.type = "gce_instance" AND resource.labels.instance_id = "123456" AND resource.labels.zone = "us-central1-a"`
	targets := resolveMonitoringTargets("my-project", filter)
	require.Len(t, targets, 1)
	assert.Equal(t,
		"https://www.googleapis.com/compute/v1/projects/my-project/zones/us-central1-a/instances/123456",
		targets[0],
	)
}

func TestResolveMonitoringTargets_GCSBucket(t *testing.T) {
	filter := `resource.type = "gcs_bucket" AND resource.labels.bucket_name = "my-bucket"`
	targets := resolveMonitoringTargets("my-project", filter)
	require.Len(t, targets, 1)
	assert.Equal(t, "gs://my-bucket", targets[0])
}

func TestResolveMonitoringTargets_CloudSQL(t *testing.T) {
	filter := `resource.type = "cloudsql_database" AND resource.labels.database_id = "my-project:my-instance"`
	targets := resolveMonitoringTargets("my-project", filter)
	require.Len(t, targets, 1)
	assert.Equal(t,
		"https://sqladmin.googleapis.com/sql/v1beta4/projects/my-project/instances/my-instance",
		targets[0],
	)
}

func TestResolveMonitoringTargets_CloudFunction(t *testing.T) {
	filter := `resource.type = "cloud_function" AND resource.labels.function_name = "my-fn" AND resource.labels.region = "us-central1"`
	targets := resolveMonitoringTargets("my-project", filter)
	require.Len(t, targets, 1)
	assert.Equal(t, "projects/my-project/locations/us-central1/functions/my-fn", targets[0])
}

func TestResolveMonitoringTargets_GenericType_NoEdge(t *testing.T) {
	// No specific resource identifiers — should return nil (per decision: accuracy).
	filter := `resource.type = "gce_instance" AND metric.type = "compute.googleapis.com/instance/cpu/utilization"`
	targets := resolveMonitoringTargets("my-project", filter)
	assert.Nil(t, targets)
}

func TestResolveMonitoringTargets_NoResourceType(t *testing.T) {
	filter := `metric.type = "compute.googleapis.com/instance/cpu/utilization"`
	targets := resolveMonitoringTargets("my-project", filter)
	assert.Nil(t, targets)
}

func TestResolveMonitoringTargets_EmptyFilter(t *testing.T) {
	targets := resolveMonitoringTargets("my-project", "")
	assert.Nil(t, targets)
}

// --- EdgeNotifiesVia (notification channels) ---

func TestAlertPolicyNotificationEdges(t *testing.T) {
	edges := alertPolicyNotificationEdges(
		"projects/p/alertPolicies/pol-1",
		[]string{
			"projects/p/notificationChannels/ch-1",
			"projects/p/notificationChannels/ch-2",
		},
	)
	require.Len(t, edges, 2)
	assert.Equal(t, kgtypes.EdgeNotifiesVia, edges[0].Relationship)
	assert.Equal(t, "projects/p/alertPolicies/pol-1", edges[0].SourceID)
	assert.Equal(t, "projects/p/notificationChannels/ch-1", edges[0].TargetID)
	assert.Equal(t, "projects/p/notificationChannels/ch-2", edges[1].TargetID)
}

func TestAlertPolicyNotificationEdges_Empty(t *testing.T) {
	assert.Empty(t, alertPolicyNotificationEdges("pol", nil))
	assert.Empty(t, alertPolicyNotificationEdges("pol", []string{}))
}

func TestBuildAlertPolicyNode_WithNotifications(t *testing.T) {
	policy := &monitoringpb.AlertPolicy{
		Name:        "projects/p/alertPolicies/pol-1",
		DisplayName: "CPU > 80%",
		Enabled:     wrapperspb.Bool(true),
		NotificationChannels: []string{
			"projects/p/notificationChannels/ch-1",
			"projects/p/notificationChannels/ch-2",
		},
	}
	seen := map[string]bool{}
	spec, edges, proxies := buildAlertPolicyNode("p", policy.GetName(), policy, seen)

	assert.Equal(t, "gcp:monitoring:alertPolicy", spec.ResourceType)

	// Should have NotifiesVia edges (no MONITORS since no conditions).
	require.Len(t, edges, 2)
	assert.Equal(t, kgtypes.EdgeNotifiesVia, edges[0].Relationship)

	// Proxy nodes for uncollected channels.
	require.Len(t, proxies, 2)
	assert.Equal(t, "gcp:monitoring:notificationChannel", proxies[0].ResourceType)
	assert.Equal(t, "false", proxies[0].Metadata["collected"])
}

func TestBuildAlertPolicyNode_DedupeProxies(t *testing.T) {
	policy := &monitoringpb.AlertPolicy{
		Name:                 "projects/p/alertPolicies/pol-1",
		NotificationChannels: []string{"projects/p/notificationChannels/ch-1"},
	}
	seen := map[string]bool{"projects/p/notificationChannels/ch-1": true}
	_, _, proxies := buildAlertPolicyNode("p", policy.GetName(), policy, seen)
	assert.Empty(t, proxies, "already-seen channel should not emit proxy")
}
