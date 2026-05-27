// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	route53types "github.com/aws/aws-sdk-go-v2/service/route53/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type route53Collector struct {
	client    *route53.Client
	region    string
	accountID string
}

func newRoute53Collector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &route53Collector{
		client:    route53.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *route53Collector) Name() string { return "route53" }

func (c *route53Collector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := route53.NewListHostedZonesPaginator(c.client, &route53.ListHostedZonesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("route53: list hosted zones: %w", err)
		}

		for _, zone := range page.HostedZones {
			content, err := json.Marshal(zone)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("route53: marshal: %w", err)
			}

			// Zone ID comes as "/hostedzone/Z1234" — strip the prefix.
			zoneID := strings.TrimPrefix(awssdk.ToString(zone.Id), "/hostedzone/")
			zoneName := awssdk.ToString(zone.Name)

			// Route53 ARNs have no region or account.
			zoneARN := fmt.Sprintf("arn:aws:route53:::hostedzone/%s", zoneID)

			resources = append(resources, cloud.ResourceSpec{
				ID:           zoneARN,
				Name:         zoneName,
				ResourceType: "route53-hostedzone",
				Region:       c.region,
				Content:      content,
				Metadata:     route53HostedZoneMetadata(zone),
			})

			// Parse alias records to discover edges to target resources.
			aliasEdges, err := c.collectAliasEdges(ctx, zoneID, zoneARN)
			if err != nil {
				return cloud.SubCollectorResult{}, err
			}
			edges = append(edges, aliasEdges...)
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// collectAliasEdges iterates record sets for a hosted zone and creates TARGETS
// edges for alias records pointing to ELB, CloudFront, or S3 resources.
// This is best-effort: target IDs may be DNS names when ARNs can't be derived.
func (c *route53Collector) collectAliasEdges(ctx context.Context, zoneID, zoneARN string) ([]cloud.EdgeSpec, error) {
	var edges []cloud.EdgeSpec

	rsPaginator := route53.NewListResourceRecordSetsPaginator(c.client, &route53.ListResourceRecordSetsInput{
		HostedZoneId: awssdk.String(zoneID),
	})
	for rsPaginator.HasMorePages() {
		page, err := rsPaginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("route53: list record sets for zone %s: %w", zoneID, err)
		}

		for _, rs := range page.ResourceRecordSets {
			if rs.AliasTarget == nil {
				continue
			}
			targetID := resolveAliasTarget(rs.AliasTarget)
			if targetID == "" {
				continue
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     zoneARN,
				TargetID:     targetID,
				Relationship: kgtypes.EdgeTargets,
			})
		}
	}

	return edges, nil
}

// resolveAliasTarget parses an alias target DNS name and returns a best-effort
// target ID (ARN or DNS name) for the referenced resource.
func resolveAliasTarget(alias *route53types.AliasTarget) string {
	dnsName := strings.TrimSuffix(awssdk.ToString(alias.DNSName), ".")
	if dnsName == "" {
		return ""
	}

	lower := strings.ToLower(dnsName)

	switch {
	// S3 website endpoints: bucketname.s3-website-region.amazonaws.com
	// or bucketname.s3-website.region.amazonaws.com
	// or bucketname.s3.amazonaws.com
	case strings.Contains(lower, "s3-website") || (strings.Contains(lower, ".s3.") && strings.HasSuffix(lower, ".amazonaws.com")):
		// Extract bucket name from the DNS prefix (first segment before .s3).
		parts := strings.SplitN(lower, ".s3", 2)
		if len(parts) > 0 && parts[0] != "" {
			return fmt.Sprintf("arn:aws:s3:::%s", parts[0])
		}
		return dnsName

	// ELB endpoints: *.elb.amazonaws.com or *.elasticloadbalancing.*
	case strings.Contains(lower, "elb.amazonaws.com") || strings.Contains(lower, "elasticloadbalancing"):
		return dnsName

	// CloudFront distributions: *.cloudfront.net
	case strings.HasSuffix(lower, ".cloudfront.net"):
		return dnsName
	}

	return ""
}

// route53HostedZoneMetadata extracts discriminating fields from a Route53 zone.
func route53HostedZoneMetadata(z route53types.HostedZone) map[string]string {
	m := make(map[string]string, 2)
	if z.Config != nil {
		m["private_zone"] = fmt.Sprintf("%t", z.Config.PrivateZone)
	}
	if z.ResourceRecordSetCount != nil {
		m["resource_record_set_count"] = fmt.Sprintf("%d", awssdk.ToInt64(z.ResourceRecordSetCount))
	}
	return m
}
