// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	ktypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type kinesisCollector struct {
	client    *kinesis.Client
	region    string
	accountID string
}

func newKinesisCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &kinesisCollector{
		client:    kinesis.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *kinesisCollector) Name() string { return "kinesis" }

func (c *kinesisCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	names, err := c.listStreamNames(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}

	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	for _, name := range names {
		res, streamEdges, err := c.describeStream(ctx, name)
		if err != nil {
			return cloud.SubCollectorResult{}, err
		}
		resources = append(resources, res)
		edges = append(edges, streamEdges...)
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// listStreamNames paginates through all Kinesis stream names.
func (c *kinesisCollector) listStreamNames(ctx context.Context) ([]string, error) {
	var names []string

	paginator := kinesis.NewListStreamsPaginator(c.client, &kinesis.ListStreamsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("kinesis: list streams: %w", err)
		}
		for _, summary := range page.StreamSummaries {
			names = append(names, awssdk.ToString(summary.StreamName))
		}
	}

	return names, nil
}

// describeStream fetches stream details and returns the resource + edges.
func (c *kinesisCollector) describeStream(ctx context.Context, streamName string) (cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	desc, err := c.client.DescribeStream(ctx, &kinesis.DescribeStreamInput{
		StreamName: awssdk.String(streamName),
	})
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("kinesis: describe stream %s: %w", streamName, err)
	}

	sd := desc.StreamDescription
	content, err := json.Marshal(sd)
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("kinesis: marshal: %w", err)
	}

	streamARN := awssdk.ToString(sd.StreamARN)

	// Extract stream name from ARN (last segment after '/').
	name := streamName
	if parts := strings.Split(streamARN, "/"); len(parts) > 1 {
		name = parts[len(parts)-1]
	}

	res := cloud.ResourceSpec{
		ID:           streamARN,
		Name:         name,
		ResourceType: "kinesis-stream",
		Region:       c.region,
		Content:      content,
		Metadata:     kinesisStreamMetadata(sd),
	}

	var edges []cloud.EdgeSpec

	// Kinesis stream → KMS key (server-side encryption)
	if sd.EncryptionType == ktypes.EncryptionTypeKms && sd.KeyId != nil {
		kmsARN := resolveKMSKeyARN(awssdk.ToString(sd.KeyId), c.region, c.accountID)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     streamARN,
			TargetID:     kmsARN,
			Relationship: kgtypes.EdgeEncryptsWith,
			Metadata:     map[string]string{"encryption_scope": "stream"},
		})
	}

	return res, edges, nil
}

// kinesisStreamMetadata extracts discriminating fields from a stream description.
func kinesisStreamMetadata(s *ktypes.StreamDescription) map[string]string {
	if s == nil {
		return nil
	}
	m := make(map[string]string, 3)
	if st := string(s.StreamStatus); st != "" {
		m["status"] = st
	}
	if s.RetentionPeriodHours != nil {
		m["retention_hours"] = fmt.Sprintf("%d", awssdk.ToInt32(s.RetentionPeriodHours))
	}
	if et := string(s.EncryptionType); et != "" {
		m["encryption_type"] = et
	}
	return m
}
