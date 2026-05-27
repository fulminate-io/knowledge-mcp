// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// flowLogsCollector enumerates NSG Flow Logs across all Network Watchers in
// the subscription. Network Watchers are used as an intermediate only — they
// are not emitted as first-class resource nodes (per decision: one watcher per
// region is auto-provisioned and carries no meaningful security edges).
type flowLogsCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newFlowLogsCollector(cred azcore.TokenCredential, subID string) *flowLogsCollector {
	return &flowLogsCollector{cred: cred, subscriptionID: subID}
}

func (c *flowLogsCollector) Name() string { return "azure-flowlogs" }

func (c *flowLogsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	watchersClient, err := armnetwork.NewWatchersClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-flowlogs: watchers client: %w", err)
	}

	flClient, err := armnetwork.NewFlowLogsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-flowlogs: flow logs client: %w", err)
	}

	var result cloud.SubCollectorResult

	// Network Watchers are listed subscription-wide via ListAll.
	pager := watchersClient.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-flowlogs: list watchers: %w", err)
		}

		for _, watcher := range page.Value {
			if watcher.ID == nil || watcher.Name == nil {
				continue
			}
			rg := resourceGroupFromID(*watcher.ID)
			if rg == "" {
				continue
			}
			c.collectFlowLogsForWatcher(ctx, flClient, rg, *watcher.Name, &result)
		}
	}

	return result, nil
}

func (c *flowLogsCollector) collectFlowLogsForWatcher(
	ctx context.Context,
	client *armnetwork.FlowLogsClient,
	resourceGroup, watcherName string,
	result *cloud.SubCollectorResult,
) {
	pager := client.NewListPager(resourceGroup, watcherName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			// Best-effort: stop listing for this watcher but continue overall.
			return
		}
		for _, fl := range page.Value {
			if fl.ID == nil || fl.Name == nil {
				continue
			}
			content, err := json.Marshal(fl)
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, flowLogResourceSpec(fl, content))
			result.Edges = append(result.Edges, flowLogEdges(fl)...)
		}
	}
}

func flowLogResourceSpec(fl *armnetwork.FlowLog, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *fl.ID,
		Name:         *fl.Name,
		ResourceType: "Microsoft.Network/networkWatchers/flowLogs",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if fl.Location != nil {
		spec.Region = *fl.Location
	}
	flowLogPropertiesMetadata(fl.Properties, spec.Metadata)
	return spec
}

func flowLogPropertiesMetadata(p *armnetwork.FlowLogPropertiesFormat, meta map[string]string) {
	if p == nil {
		return
	}
	if p.Enabled != nil {
		meta["enabled"] = fmt.Sprintf("%t", *p.Enabled)
	}
	if p.Format != nil && p.Format.Version != nil {
		meta["formatVersion"] = fmt.Sprintf("%d", *p.Format.Version)
	}
	if p.RetentionPolicy != nil && p.RetentionPolicy.Days != nil {
		meta["retentionDays"] = fmt.Sprintf("%d", *p.RetentionPolicy.Days)
	}
	if p.ProvisioningState != nil {
		meta["provisioningState"] = string(*p.ProvisioningState)
	}
}

// flowLogEdges emits:
//   - MONITORS (flow log → NSG, via TargetResourceID)
//   - SINKS_TO (flow log → storage account, via StorageID)
//   - SINKS_TO (flow log → Log Analytics workspace, via traffic analytics)
func flowLogEdges(fl *armnetwork.FlowLog) []cloud.EdgeSpec {
	if fl.Properties == nil {
		return nil
	}
	var edges []cloud.EdgeSpec

	if fl.Properties.TargetResourceID != nil && *fl.Properties.TargetResourceID != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *fl.ID,
			TargetID:     *fl.Properties.TargetResourceID,
			Relationship: kgtypes.EdgeMonitors,
		})
	}
	if fl.Properties.StorageID != nil && *fl.Properties.StorageID != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *fl.ID,
			TargetID:     *fl.Properties.StorageID,
			Relationship: kgtypes.EdgeSinksTo,
		})
	}
	if ta := fl.Properties.FlowAnalyticsConfiguration; ta != nil && ta.NetworkWatcherFlowAnalyticsConfiguration != nil {
		if wsID := ta.NetworkWatcherFlowAnalyticsConfiguration.WorkspaceResourceID; wsID != nil && *wsID != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *fl.ID,
				TargetID:     *wsID,
				Relationship: kgtypes.EdgeSinksTo,
			})
		}
	}

	return edges
}

// resourceGroupFromID extracts the resource group name from any Azure resource
// ID of the form /subscriptions/{sub}/resourceGroups/{rg}/providers/...
func resourceGroupFromID(id string) string {
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			return parts[i+1]
		}
	}
	return ""
}
