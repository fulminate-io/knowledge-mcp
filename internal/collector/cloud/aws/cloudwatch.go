// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

type cloudwatchCollector struct {
	cwClient   *cloudwatch.Client
	logsClient *cloudwatchlogs.Client
	region     string
	accountID  string
}

func newCloudwatchCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &cloudwatchCollector{
		cwClient:   cloudwatch.NewFromConfig(cfg),
		logsClient: cloudwatchlogs.NewFromConfig(cfg),
		region:     region,
		accountID:  accountID,
	}
}

func (c *cloudwatchCollector) Name() string { return "cloudwatch" }

func (c *cloudwatchCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	// Collect CloudWatch log groups.
	logGroups, err := c.collectLogGroups(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}
	resources = append(resources, logGroups...)

	// Collect CloudWatch alarms.
	alarmResources, alarmEdges, err := c.collectAlarms(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}
	resources = append(resources, alarmResources...)
	edges = append(edges, alarmEdges...)

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// collectLogGroups paginates through all CloudWatch log groups.
func (c *cloudwatchCollector) collectLogGroups(ctx context.Context) ([]cloud.ResourceSpec, error) {
	var resources []cloud.ResourceSpec

	paginator := cloudwatchlogs.NewDescribeLogGroupsPaginator(c.logsClient, &cloudwatchlogs.DescribeLogGroupsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cloudwatch: describe log groups: %w", err)
		}
		for _, lg := range page.LogGroups {
			res, err := c.buildLogGroupResource(lg)
			if err != nil {
				return nil, err
			}
			resources = append(resources, res)
		}
	}

	return resources, nil
}

// buildLogGroupResource creates a ResourceSpec for a single log group.
func (c *cloudwatchCollector) buildLogGroupResource(lg logstypes.LogGroup) (cloud.ResourceSpec, error) {
	content, err := json.Marshal(lg)
	if err != nil {
		return cloud.ResourceSpec{}, fmt.Errorf("cloudwatch: marshal log group: %w", err)
	}

	lgARN := awssdk.ToString(lg.Arn)
	// Log group ARNs end with ":*" — strip it for the node ID.
	lgARN = strings.TrimSuffix(lgARN, ":*")

	name := awssdk.ToString(lg.LogGroupName)

	return cloud.ResourceSpec{
		ID:           lgARN,
		Name:         name,
		ResourceType: "cloudwatch-loggroup",
		Region:       c.region,
		Content:      content,
		Metadata:     cloudwatchLogGroupMetadata(lg),
	}, nil
}

// cloudwatchLogGroupMetadata extracts discriminating fields from a log group.
func cloudwatchLogGroupMetadata(lg logstypes.LogGroup) map[string]string {
	m := make(map[string]string, 2)
	if lg.RetentionInDays != nil {
		m["retention_days"] = fmt.Sprintf("%d", awssdk.ToInt32(lg.RetentionInDays))
	}
	if k := awssdk.ToString(lg.KmsKeyId); k != "" {
		m["kms_key_id"] = k
	}
	return m
}
