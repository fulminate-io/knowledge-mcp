// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cttypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type cloudtrailCollector struct {
	client    *cloudtrail.Client
	region    string
	accountID string
}

func newCloudTrailCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &cloudtrailCollector{
		client:    cloudtrail.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *cloudtrailCollector) Name() string { return "cloudtrail" }

func (c *cloudtrailCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	out, err := c.client.DescribeTrails(ctx, &cloudtrail.DescribeTrailsInput{
		IncludeShadowTrails: awssdk.Bool(false),
	})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("cloudtrail: describe trails: %w", err)
	}

	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	for _, trail := range out.TrailList {
		trailARN := awssdk.ToString(trail.TrailARN)
		if trailARN == "" {
			continue
		}

		content, err := json.Marshal(trail)
		if err != nil {
			continue
		}

		resources = append(resources, cloud.ResourceSpec{
			ID:           trailARN,
			Name:         awssdk.ToString(trail.Name),
			ResourceType: "cloudtrail-trail",
			Region:       c.region,
			Content:      content,
			Metadata:     cloudtrailTrailMetadata(trail),
		})

		edges = append(edges, c.trailEdges(trailARN, trail)...)
	}

	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

func (c *cloudtrailCollector) trailEdges(
	trailARN string, trail cttypes.Trail,
) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Trail → S3 bucket (SINKS_TO)
	if bucket := awssdk.ToString(trail.S3BucketName); bucket != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     trailARN,
			TargetID:     fmt.Sprintf("arn:aws:s3:::%s", bucket),
			Relationship: kgtypes.EdgeSinksTo,
			Metadata:     map[string]string{"destination_type": "s3"},
		})
	}

	// Trail → CloudWatch Logs log group (SINKS_TO)
	if logGroup := awssdk.ToString(trail.CloudWatchLogsLogGroupArn); logGroup != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     trailARN,
			TargetID:     logGroup,
			Relationship: kgtypes.EdgeSinksTo,
			Metadata:     map[string]string{"destination_type": "cloudwatch"},
		})
	}

	// Trail → KMS key (ENCRYPTS_WITH)
	if kmsKeyID := awssdk.ToString(trail.KmsKeyId); kmsKeyID != "" {
		kmsARN := resolveKMSKeyARN(kmsKeyID, c.region, c.accountID)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     trailARN,
			TargetID:     kmsARN,
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}

	return edges
}

// cloudtrailTrailMetadata extracts discriminating fields from a CloudTrail trail.
func cloudtrailTrailMetadata(t cttypes.Trail) map[string]string {
	m := make(map[string]string, 3)
	if t.IsMultiRegionTrail != nil {
		m["multi_region"] = fmt.Sprintf("%t", awssdk.ToBool(t.IsMultiRegionTrail))
	}
	if hr := awssdk.ToString(t.HomeRegion); hr != "" {
		m["home_region"] = hr
	}
	if t.LogFileValidationEnabled != nil {
		m["log_file_validation_enabled"] = fmt.Sprintf("%t", awssdk.ToBool(t.LogFileValidationEnabled))
	}
	return m
}
