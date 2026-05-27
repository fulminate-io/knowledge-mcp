// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type flowLogsCollector struct {
	client    *ec2.Client
	region    string
	accountID string
}

func newFlowLogsCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &flowLogsCollector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *flowLogsCollector) Name() string { return "flow-logs" }

func (c *flowLogsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := ec2.NewDescribeFlowLogsPaginator(c.client, &ec2.DescribeFlowLogsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("flow-logs: describe: %w", err)
		}
		for _, fl := range page.FlowLogs {
			res, flEdges := c.buildFlowLog(fl)
			if res == nil {
				continue
			}
			resources = append(resources, *res)
			edges = append(edges, flEdges...)
		}
	}

	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

func (c *flowLogsCollector) buildFlowLog(fl ec2types.FlowLog) (*cloud.ResourceSpec, []cloud.EdgeSpec) {
	flID := awssdk.ToString(fl.FlowLogId)
	if flID == "" {
		return nil, nil
	}

	flARN := fmt.Sprintf("arn:aws:ec2:%s:%s:flow-log/%s", c.region, c.accountID, flID)

	content, err := json.Marshal(fl)
	if err != nil {
		return nil, nil
	}

	res := cloud.ResourceSpec{
		ID:           flARN,
		Name:         flID,
		ResourceType: "flow-log",
		Region:       c.region,
		Content:      content,
		Metadata:     flowLogMetadata(fl),
	}

	var edges []cloud.EdgeSpec

	// FlowLog → destination (SINKS_TO)
	destARN := c.resolveDestination(fl)
	if destARN != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     flARN,
			TargetID:     destARN,
			Relationship: kgtypes.EdgeSinksTo,
		})
	}

	// FlowLog → source resource (MONITORS)
	if resourceID := awssdk.ToString(fl.ResourceId); resourceID != "" {
		sourceARN := c.resolveResourceARN(resourceID)
		if sourceARN != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     flARN,
				TargetID:     sourceARN,
				Relationship: kgtypes.EdgeMonitors,
			})
		}
	}

	return &res, edges
}

// resolveDestination returns the ARN of the flow log destination.
func (c *flowLogsCollector) resolveDestination(fl ec2types.FlowLog) string {
	if dest := awssdk.ToString(fl.LogDestination); dest != "" {
		return dest
	}
	// Legacy CloudWatch log group: construct ARN from LogGroupName.
	if lgName := awssdk.ToString(fl.LogGroupName); lgName != "" {
		return fmt.Sprintf("arn:aws:logs:%s:%s:log-group:%s", c.region, c.accountID, lgName)
	}
	return ""
}

// resolveResourceARN converts a VPC/subnet/ENI resource ID to an ARN.
func (c *flowLogsCollector) resolveResourceARN(resourceID string) string {
	switch {
	case len(resourceID) > 4 && resourceID[:4] == "vpc-":
		return ec2ARN(c.region, c.accountID, "vpc", resourceID)
	case len(resourceID) > 7 && resourceID[:7] == "subnet-":
		return ec2ARN(c.region, c.accountID, "subnet", resourceID)
	case len(resourceID) > 4 && resourceID[:4] == "eni-":
		return ec2ARN(c.region, c.accountID, "network-interface", resourceID)
	default:
		return ""
	}
}

// flowLogMetadata extracts discriminating fields from a flow log.
func flowLogMetadata(fl ec2types.FlowLog) map[string]string {
	m := make(map[string]string, 3)
	if t := string(fl.TrafficType); t != "" {
		m["traffic_type"] = t
	}
	if r := awssdk.ToString(fl.ResourceId); r != "" {
		m["resource_id"] = r
	}
	if d := string(fl.LogDestinationType); d != "" {
		m["log_destination_type"] = d
	}
	return m
}
