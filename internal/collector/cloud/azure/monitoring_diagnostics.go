// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectDiagnosticSettings enumerates subscription-level diagnostic settings
// that route the Activity Log to sinks (storage, Event Hub, Log Analytics).
// Per decision, per-resource diagnostic settings are NOT collected — that
// would require a ListByResource call for every resource in the subscription.
func (c *monitoringCollector) collectDiagnosticSettings(ctx context.Context, result *cloud.SubCollectorResult) error {
	client, err := armmonitor.NewDiagnosticSettingsClient(c.cred, nil)
	if err != nil {
		return fmt.Errorf("azure-monitoring: diagnostic settings client: %w", err)
	}

	subscriptionURI := "/subscriptions/" + c.subscriptionID
	pager := client.NewListPager(subscriptionURI, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			// Best-effort: a missing Microsoft.Insights provider registration
			// or insufficient permissions should not fail the whole collector.
			return nil //nolint:nilerr // best-effort: skip when provider not registered
		}
		for _, ds := range page.Value {
			if ds.ID == nil || ds.Name == nil {
				continue
			}
			content, err := json.Marshal(buildDiagnosticSettingContent(ds))
			if err != nil {
				continue // best-effort: this collector is already documented best-effort (see line 33)
			}
			result.Resources = append(result.Resources, diagnosticSettingResourceSpec(ds, content))
			result.Edges = append(result.Edges, diagnosticSettingEdges(ds)...)
		}
	}

	return nil
}

// diagnosticSettingContent is the curated wire shape for
// Microsoft.Insights/diagnosticSettings. Curated projection of
// *armmonitor.DiagnosticSettingsResource (collector-owned, decoupled from
// SDK version). Logs/Metrics arrays are collapsed to *bool flags mirroring
// the metadata extractors at monitoring_diagnostics.go:63-64.
//
// Excluded: Logs []*LogSettings, Metrics []*MetricSettings — collapsed to
// the LogsEnabled/MetricsEnabled bool flags above; no reader needs the full
// arrays.
type diagnosticSettingContent struct {
	ID         string                              `json:"id"`
	Name       string                              `json:"name"`
	Properties *diagnosticSettingContentProperties `json:"properties,omitempty"`
}

type diagnosticSettingContentProperties struct {
	LogAnalyticsDestinationType string `json:"logAnalyticsDestinationType,omitempty"`
	StorageAccountID            string `json:"storageAccountId,omitempty"`
	WorkspaceID                 string `json:"workspaceId,omitempty"`
	EventHubAuthorizationRuleID string `json:"eventHubAuthorizationRuleId,omitempty"`
	MarketplacePartnerID        string `json:"marketplacePartnerId,omitempty"`
	LogsEnabled                 *bool  `json:"logsEnabled,omitempty"`
	MetricsEnabled              *bool  `json:"metricsEnabled,omitempty"`
}

// buildDiagnosticSettingContent projects an *armmonitor.DiagnosticSettingsResource
// into the diagnosticSettingContent wire shape. Nil-safe at every level.
func buildDiagnosticSettingContent(ds *armmonitor.DiagnosticSettingsResource) diagnosticSettingContent {
	out := diagnosticSettingContent{}
	if ds == nil {
		return out
	}
	if ds.ID != nil {
		out.ID = *ds.ID
	}
	if ds.Name != nil {
		out.Name = *ds.Name
	}
	if ds.Properties != nil {
		out.Properties = projectDiagnosticSettingProperties(ds.Properties)
	}
	return out
}

func projectDiagnosticSettingProperties(p *armmonitor.DiagnosticSettings) *diagnosticSettingContentProperties {
	props := &diagnosticSettingContentProperties{}
	if p.LogAnalyticsDestinationType != nil {
		props.LogAnalyticsDestinationType = *p.LogAnalyticsDestinationType
	}
	if p.StorageAccountID != nil {
		props.StorageAccountID = *p.StorageAccountID
	}
	if p.WorkspaceID != nil {
		props.WorkspaceID = *p.WorkspaceID
	}
	if p.EventHubAuthorizationRuleID != nil {
		props.EventHubAuthorizationRuleID = *p.EventHubAuthorizationRuleID
	}
	if p.MarketplacePartnerID != nil {
		props.MarketplacePartnerID = *p.MarketplacePartnerID
	}
	logsEnabled := diagnosticSettingHasEnabledLogs(p.Logs)
	props.LogsEnabled = &logsEnabled
	metricsEnabled := diagnosticSettingHasEnabledMetrics(p.Metrics)
	props.MetricsEnabled = &metricsEnabled
	return props
}

func diagnosticSettingResourceSpec(ds *armmonitor.DiagnosticSettingsResource, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *ds.ID,
		Name:         *ds.Name,
		ResourceType: "Microsoft.Insights/diagnosticSettings",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if ds.Properties != nil {
		if ds.Properties.LogAnalyticsDestinationType != nil {
			spec.Metadata["logAnalyticsDestinationType"] = *ds.Properties.LogAnalyticsDestinationType
		}
		spec.Metadata["logsEnabled"] = fmt.Sprintf("%t", diagnosticSettingHasEnabledLogs(ds.Properties.Logs))
		spec.Metadata["metricsEnabled"] = fmt.Sprintf("%t", diagnosticSettingHasEnabledMetrics(ds.Properties.Metrics))
	}
	return spec
}

func diagnosticSettingHasEnabledLogs(logs []*armmonitor.LogSettings) bool {
	for _, l := range logs {
		if l != nil && l.Enabled != nil && *l.Enabled {
			return true
		}
	}
	return false
}

func diagnosticSettingHasEnabledMetrics(metrics []*armmonitor.MetricSettings) bool {
	for _, m := range metrics {
		if m != nil && m.Enabled != nil && *m.Enabled {
			return true
		}
	}
	return false
}

// diagnosticSettingEdges emits SINKS_TO from the diagnostic setting to each
// configured destination (storage account, Event Hub auth rule, Log Analytics
// workspace, marketplace partner).
func diagnosticSettingEdges(ds *armmonitor.DiagnosticSettingsResource) []cloud.EdgeSpec {
	if ds.Properties == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	p := ds.Properties

	appendSink := func(target *string) {
		if target == nil || *target == "" {
			return
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *ds.ID,
			TargetID:     *target,
			Relationship: kgtypes.EdgeSinksTo,
		})
	}

	appendSink(p.StorageAccountID)
	appendSink(p.WorkspaceID)
	appendSink(p.EventHubAuthorizationRuleID)
	appendSink(p.MarketplacePartnerID)

	return edges
}
