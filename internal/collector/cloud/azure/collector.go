// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func init() {
	collector.Register(&AzureCollector{})
}

// AzureCollector implements collector.Collector for Azure subscriptions.
// Auth is from environment via DefaultAzureCredential.
type AzureCollector struct{}

// Name returns "azure" — the collector type used for registry lookup.
func (c *AzureCollector) Name() string { return "azure" }

// Collect discovers Azure resources in a subscription and returns them as graph
// nodes and edges. The id parameter is the subscription ID; if empty it falls
// back to the AZURE_SUBSCRIPTION_ID environment variable.
func (c *AzureCollector) Collect(ctx context.Context, id string, opts collector.CollectOptions) (*collectorwire.CollectResult, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("azure: credential: %w", err)
	}

	subscriptionID := id
	if subscriptionID == "" {
		subscriptionID = os.Getenv("AZURE_SUBSCRIPTION_ID")
	}
	if subscriptionID == "" {
		return nil, fmt.Errorf("azure: subscription ID required (pass as id or set AZURE_SUBSCRIPTION_ID)")
	}

	subs := buildSubCollectors(cred, subscriptionID)

	nodes, edges, targets, err := cloud.RunSubCollectors(ctx, subs, cloud.RunOptions{
		OnProgress: opts.OnProgress,
	})
	if err != nil {
		// RunSubCollectors returns partial results on error — log but continue.
		slog.Warn("azure: partial collection errors", "error", err)
	}

	// Process cascade targets (e.g. AKS → k8s).
	cs := cloud.CascadeSetFrom(ctx)
	rm := cloud.ResolutionMapFrom(ctx)
	for _, t := range targets {
		if cs != nil && !cs.Mark(t.Collector, t.ID) {
			continue
		}
		if rm != nil {
			rm.Record(t.ID, t.ResolutionID)
		}
		if err := collector.Collect(ctx, t.Collector, t.ID, opts); err != nil {
			slog.Warn("azure: cascade error",
				"collector", t.Collector, "target", t.ID, "error", err)
		}
	}

	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCloud,
		GraphName: subscriptionID,
		Nodes:     nodes,
		Edges:     edges,
		// The enumeration was complete only if no subcollector failed. A partial
		// enumeration must never assert a complete walk: walk_complete is what arms
		// the server's whole-remainder deletion basis, so a resource this run failed
		// to READ would be named as deleted. The Warn above stays the operator signal.
		WalkComplete: err == nil,
	}, nil
}

// buildSubCollectors creates all Azure subcollectors with shared credentials.
func buildSubCollectors(cred azcore.TokenCredential, subID string) []cloud.SubCollector {
	return []cloud.SubCollector{
		newVMCollector(cred, subID),
		newVMSSCollector(cred, subID),
		newVNetCollector(cred, subID),
		newNSGCollector(cred, subID),
		newLBCollector(cred, subID),
		newAKSCollector(cred, subID),
		newACRCollector(cred, subID),
		newIdentityCollector(cred, subID),
		newSQLCollector(cred, subID),
		newStorageCollector(cred, subID),
		newKVCollector(cred, subID),
		newDNSCollector(cred, subID),
		newCosmosCollector(cred, subID),
		newMonitoringCollector(cred, subID),
		newFunctionsCollector(cred, subID),
		newAppServiceCollector(cred, subID),
		newServiceBusCollector(cred, subID),
		newAppGatewayCollector(cred, subID),
		newFrontDoorCollector(cred, subID),
		newEventHubCollector(cred, subID),
		newFirewallCollector(cred, subID),
		newAPIMCollector(cred, subID),
		newLogicAppsCollector(cred, subID),
		newEventGridCollector(cred, subID),
		newPrivateEndpointCollector(cred, subID),
		newPrivateDNSCollector(cred, subID),
		newMetricAlertsCollector(cred, subID),
		newVNetPeeringCollector(cred, subID),
		newDisksCollector(cred, subID),
		newRedisCollector(cred, subID),
		newNatGatewayCollector(cred, subID),
		newCertificatesCollector(cred, subID),
		newSearchCollector(cred, subID),
		newFlowLogsCollector(cred, subID),
		newSynapseCollector(cred, subID),
		newAADGroupsCollector(cred),
	}
}
