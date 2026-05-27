// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func init() {
	collector.Register(&AWSCollector{})
}

// AWSCollector collects AWS infrastructure resources.
// Auth is handled entirely by the environment via config.LoadDefaultConfig.
// The graph name is the AWS account ID discovered from STS GetCallerIdentity.
type AWSCollector struct{}

func (c *AWSCollector) Name() string { return "aws" }

func (c *AWSCollector) Collect(ctx context.Context, _ string, opts collector.CollectOptions) (*collectorwire.CollectResult, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("aws: load config: %w", err)
	}

	// Discover account ID — this becomes the graph name.
	stsClient := sts.NewFromConfig(cfg)
	identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("aws: get caller identity: %w", err)
	}
	accountID := *identity.Account
	region := cfg.Region

	slog.Info("aws: collecting", "account", accountID, "region", region)

	subs := buildSubCollectors(cfg, region, accountID)

	nodes, edges, targets, err := cloud.RunSubCollectors(ctx, subs, cloud.RunOptions{
		OnProgress: opts.OnProgress,
	})
	if err != nil {
		slog.Warn("aws: partial collection errors", "error", err)
	}

	// Process cascade targets (e.g., EKS → k8s collector).
	cs := cloud.CascadeSetFrom(ctx)
	rm := cloud.ResolutionMapFrom(ctx)
	for _, target := range targets {
		if cs != nil && !cs.Mark(target.Collector, target.ID) {
			continue // already visited
		}
		if rm != nil {
			rm.Record(target.ID, target.ResolutionID)
		}
		slog.Info("aws: cascading", "collector", target.Collector, "id", target.ID)
		if cascadeErr := collector.Collect(ctx, target.Collector, target.ID, opts); cascadeErr != nil {
			slog.Warn("aws: cascade failed", "collector", target.Collector, "id", target.ID, "error", cascadeErr)
		}
	}

	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCloud,
		GraphName: accountID,
		Nodes:     nodes,
		Edges:     edges,
	}, nil
}

// buildSubCollectors creates all AWS subcollectors with the given config.
// This is the single wiring point — each phase adds its subcollectors here.
func buildSubCollectors(cfg awssdk.Config, region, accountID string) []cloud.SubCollector {
	return []cloud.SubCollector{
		// Network
		newVPCCollector(cfg, region, accountID),
		newSubnetCollector(cfg, region, accountID),
		newSecurityGroupCollector(cfg, region, accountID),
		newNetworkACLCollector(cfg, region, accountID),
		newVpcPeeringCollector(cfg, region, accountID),
		newTransitGatewayCollector(cfg, region, accountID),
		newVpcEndpointCollector(cfg, region, accountID),
		// Compute & Containers
		newEC2Collector(cfg, region, accountID),
		newLambdaCollector(cfg, region, accountID),
		newEKSCollector(cfg, region, accountID),
		newECSCollector(cfg, region, accountID),
		// Data & Storage
		newRDSCollector(cfg, region, accountID),
		newRedshiftCollector(cfg, region, accountID),
		newS3Collector(cfg, region, accountID),
		newRoute53Collector(cfg, region, accountID),
		// Identity & Load Balancing
		newIAMCollector(cfg, accountID),
		newELBv2Collector(cfg, region, accountID),
		// Public-entry surfaces (topology/public_exposure analyzer seeds)
		newAPIGatewayCollector(cfg, region),
		newAPIGatewayV2Collector(cfg, region),
		// P0: Core infrastructure
		newECRCollector(cfg, region, accountID),
		newSQSCollector(cfg, region, accountID),
		newEBSCollector(cfg, region, accountID),
		// P1: Event-driven and encryption topology
		newSNSCollector(cfg, region, accountID),
		newDynamoDBCollector(cfg, region, accountID),
		newKMSCollector(cfg, region, accountID),
		// SG reachability v2: cache/search/filesystem attachment coverage
		newElastiCacheCollector(cfg, region, accountID),
		newOpenSearchCollector(cfg, region, accountID),
		newEFSCollector(cfg, region, accountID),
		// Tier 1: CDN, secrets, network egress
		newCloudfrontCollector(cfg, region, accountID),
		newSecretsManagerCollector(cfg, region, accountID),
		newNATGatewayCollector(cfg, region, accountID),
		// Tier 2: Event routing, certificates, internet egress
		newEventBridgeCollector(cfg, region, accountID),
		newACMCollector(cfg, region, accountID),
		newIGWCollector(cfg, region, accountID),
		// Tier 3: Streaming, orchestration, monitoring, email
		newKinesisCollector(cfg, region, accountID),
		newStepFunctionsCollector(cfg, region),
		newCloudwatchCollector(cfg, region, accountID),
		newSESCollector(cfg, region, accountID),
		// Observability: flow logs, audit trails
		newFlowLogsCollector(cfg, region, accountID),
		newCloudTrailCollector(cfg, region, accountID),
	}
}
