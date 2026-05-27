// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type metricAlertsCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newMetricAlertsCollector(cred azcore.TokenCredential, subID string) *metricAlertsCollector {
	return &metricAlertsCollector{cred: cred, subscriptionID: subID}
}

func (c *metricAlertsCollector) Name() string { return "azure-monitoring-alerts" }

func (c *metricAlertsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armmonitor.NewMetricAlertsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-monitoring-alerts: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListBySubscriptionPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-monitoring-alerts: list: %w", err)
		}

		for _, alert := range page.Value {
			if alert.ID == nil || alert.Name == nil {
				continue
			}

			content, err := json.Marshal(buildMetricAlertContent(alert))
			if err != nil {
				return result, fmt.Errorf("azure-monitoring-alerts: marshal alert content: %w", err)
			}

			result.Resources = append(result.Resources, metricAlertResourceSpec(alert, content))
			result.Edges = append(result.Edges, metricAlertEdges(alert)...)
		}
	}

	return result, nil
}

// metricAlertContent is the curated wire shape for
// Microsoft.Insights/metricAlerts. Curated projection of
// *armmonitor.MetricAlertResource (collector-owned, decoupled from SDK
// version). Severity kept as *int32 so nil and zero remain
// distinguishable. Scopes is denormalized from []*string to []string with
// nil-skip in the builder.
//
// Excluded: criteria expression body, action groups — no reader.
type metricAlertContent struct {
	ID         string                        `json:"id"`
	Name       string                        `json:"name"`
	Location   string                        `json:"location,omitempty"`
	Properties *metricAlertContentProperties `json:"properties,omitempty"`
}

type metricAlertContentProperties struct {
	Severity *int32   `json:"severity,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	Enabled  *bool    `json:"enabled,omitempty"`
}

// buildMetricAlertContent projects an *armmonitor.MetricAlertResource into
// the metricAlertContent wire shape. Nil-safe at every level.
func buildMetricAlertContent(alert *armmonitor.MetricAlertResource) metricAlertContent {
	out := metricAlertContent{}
	if alert == nil {
		return out
	}
	if alert.ID != nil {
		out.ID = *alert.ID
	}
	if alert.Name != nil {
		out.Name = *alert.Name
	}
	if alert.Location != nil {
		out.Location = *alert.Location
	}
	if alert.Properties != nil {
		props := &metricAlertContentProperties{}
		if alert.Properties.Severity != nil {
			v := *alert.Properties.Severity
			props.Severity = &v
		}
		for _, scope := range alert.Properties.Scopes {
			if scope == nil || *scope == "" {
				continue
			}
			props.Scopes = append(props.Scopes, *scope)
		}
		if alert.Properties.Enabled != nil {
			b := *alert.Properties.Enabled
			props.Enabled = &b
		}
		out.Properties = props
	}
	return out
}

func metricAlertResourceSpec(alert *armmonitor.MetricAlertResource, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *alert.ID,
		Name:         *alert.Name,
		ResourceType: "Microsoft.Insights/metricAlerts",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if alert.Location != nil {
		spec.Region = *alert.Location
	}
	if alert.Properties != nil && alert.Properties.Severity != nil {
		spec.Metadata["severity"] = fmt.Sprintf("%d", *alert.Properties.Severity)
	}
	return spec
}

// metricAlertEdges emits MONITORS edges from the alert to each resource in
// its scope list. Each scope entry is an Azure resource ID.
func metricAlertEdges(alert *armmonitor.MetricAlertResource) []cloud.EdgeSpec {
	if alert.Properties == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, scope := range alert.Properties.Scopes {
		if scope == nil || *scope == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *alert.ID,
			TargetID:     *scope,
			Relationship: kgtypes.EdgeMonitors,
		})
	}
	return edges
}
