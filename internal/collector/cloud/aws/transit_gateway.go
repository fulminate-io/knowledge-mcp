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

type transitGatewayCollector struct {
	client    *ec2.Client
	region    string
	accountID string
}

func newTransitGatewayCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &transitGatewayCollector{
		client:    ec2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *transitGatewayCollector) Name() string { return "transit-gateway" }

func (c *transitGatewayCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	tgws, err := c.collectTransitGateways(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}
	attResources, attEdges, err := c.collectAttachments(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}
	return cloud.SubCollectorResult{
		Resources: append(tgws, attResources...),
		Edges:     attEdges,
	}, nil
}

func (c *transitGatewayCollector) collectTransitGateways(ctx context.Context) ([]cloud.ResourceSpec, error) {
	var resources []cloud.ResourceSpec
	pager := ec2.NewDescribeTransitGatewaysPaginator(c.client, &ec2.DescribeTransitGatewaysInput{})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("transit-gateway: describe tgws: %w", err)
		}
		for _, tgw := range page.TransitGateways {
			content, err := json.Marshal(tgw)
			if err != nil {
				return nil, fmt.Errorf("transit-gateway: marshal tgw: %w", err)
			}
			id := awssdk.ToString(tgw.TransitGatewayId)
			if id == "" {
				continue
			}
			resources = append(resources, cloud.ResourceSpec{
				ID:           ec2ARN(c.region, c.accountID, "transit-gateway", id),
				Name:         nameTag(tgw.Tags, id),
				ResourceType: "transit-gateway",
				Region:       c.region,
				Content:      content,
				Metadata:     transitGatewayMetadata(tgw),
			})
		}
	}
	return resources, nil
}

func (c *transitGatewayCollector) collectAttachments(ctx context.Context) ([]cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)
	pager := ec2.NewDescribeTransitGatewayAttachmentsPaginator(c.client, &ec2.DescribeTransitGatewayAttachmentsInput{})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("transit-gateway: describe attachments: %w", err)
		}
		for _, att := range page.TransitGatewayAttachments {
			content, err := json.Marshal(att)
			if err != nil {
				return nil, nil, fmt.Errorf("transit-gateway: marshal attachment: %w", err)
			}
			attID := awssdk.ToString(att.TransitGatewayAttachmentId)
			tgwID := awssdk.ToString(att.TransitGatewayId)
			if attID == "" || tgwID == "" {
				continue
			}
			attARN := ec2ARN(c.region, c.accountID, "transit-gateway-attachment", attID)
			resources = append(resources, cloud.ResourceSpec{
				ID:           attARN,
				Name:         attID,
				ResourceType: "transit-gateway-attachment",
				Region:       c.region,
				Content:      content,
				Metadata:     transitGatewayAttachmentMetadata(att),
			})

			tgwARN := ec2ARN(c.region, c.accountID, "transit-gateway", tgwID)
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     attARN,
				TargetID:     tgwARN,
				Relationship: kgtypes.EdgeUsesNetwork,
			})

			if string(att.ResourceType) == "vpc" {
				if resourceID := awssdk.ToString(att.ResourceId); resourceID != "" {
					vpcARN := ec2ARN(c.region, c.accountID, "vpc", resourceID)
					edges = append(edges, cloud.EdgeSpec{
						SourceID:     vpcARN,
						TargetID:     tgwARN,
						Relationship: kgtypes.EdgeRoutesVia,
					})
				}
			}
		}
	}
	return resources, edges, nil
}

// transitGatewayMetadata extracts discriminating fields from a Transit Gateway.
func transitGatewayMetadata(t ec2types.TransitGateway) map[string]string {
	m := make(map[string]string, 2)
	if s := string(t.State); s != "" {
		m["state"] = s
	}
	if t.Options != nil && t.Options.AmazonSideAsn != nil {
		m["amazon_side_asn"] = fmt.Sprintf("%d", awssdk.ToInt64(t.Options.AmazonSideAsn))
	}
	return m
}

// transitGatewayAttachmentMetadata extracts discriminating fields from an attachment.
func transitGatewayAttachmentMetadata(a ec2types.TransitGatewayAttachment) map[string]string {
	m := make(map[string]string, 3)
	if rt := string(a.ResourceType); rt != "" {
		m["resource_type"] = rt
	}
	if v := awssdk.ToString(a.ResourceId); v != "" {
		m["resource_id"] = v
	}
	if s := string(a.State); s != "" {
		m["state"] = s
	}
	return m
}
