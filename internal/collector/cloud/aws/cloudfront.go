// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type cloudfrontCollector struct {
	client    *cloudfront.Client
	region    string
	accountID string
}

// newCloudfrontCollector creates a CloudFront subcollector.
func newCloudfrontCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &cloudfrontCollector{
		client:    cloudfront.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *cloudfrontCollector) Name() string { return "cloudfront" }

func (c *cloudfrontCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := cloudfront.NewListDistributionsPaginator(c.client, &cloudfront.ListDistributionsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("cloudfront: list distributions: %w", err)
		}
		if page.DistributionList == nil {
			continue
		}

		for _, dist := range page.DistributionList.Items {
			content, err := json.Marshal(dist)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("cloudfront: marshal: %w", err)
			}

			distARN := awssdk.ToString(dist.ARN)
			distID := awssdk.ToString(dist.Id)

			resources = append(resources, cloud.ResourceSpec{
				ID:           distARN,
				Name:         distID,
				ResourceType: "cloudfront-distribution",
				Region:       "global",
				Content:      content,
				Metadata:     cloudfrontDistributionMetadata(dist),
			})

			edges = append(edges, distributionEdges(distARN, dist)...)
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// distributionEdges extracts ROUTES_TO and USES_CERT edges from a CloudFront distribution.
func distributionEdges(distARN string, dist cftypes.DistributionSummary) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// USES_CERT: distribution → ACM certificate.
	if vc := dist.ViewerCertificate; vc != nil {
		if certARN := awssdk.ToString(vc.ACMCertificateArn); certARN != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     distARN,
				TargetID:     certARN,
				Relationship: kgtypes.EdgeUsesCert,
			})
		}
	}

	// PROTECTS: WAF WebACL → distribution.
	if webACLID := awssdk.ToString(dist.WebACLId); webACLID != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     webACLID,
			TargetID:     distARN,
			Relationship: kgtypes.EdgeProtects,
		})
	}

	// ROUTES_TO: distribution → origins.
	if dist.Origins == nil {
		return edges
	}
	for _, origin := range dist.Origins.Items {
		targetID := resolveOriginTarget(awssdk.ToString(origin.DomainName))
		if targetID == "" {
			continue
		}
		meta := map[string]string{
			"origin_domain": awssdk.ToString(origin.DomainName),
		}
		if path := awssdk.ToString(origin.OriginPath); path != "" {
			meta["origin_path"] = path
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     distARN,
			TargetID:     targetID,
			Relationship: kgtypes.EdgeRoutesTo,
			Metadata:     meta,
		})
	}
	return edges
}

// resolveOriginTarget maps a CloudFront origin DomainName to a target resource ID.
// Recognizes S3 buckets, ALB/ELB, and API Gateway origins.
func resolveOriginTarget(domain string) string {
	if domain == "" {
		return ""
	}

	// S3 bucket origin: <bucket>.s3.amazonaws.com or <bucket>.s3.<region>.amazonaws.com
	if strings.Contains(domain, ".s3.") || strings.HasSuffix(domain, ".s3.amazonaws.com") {
		bucket := strings.Split(domain, ".s3")[0]
		return fmt.Sprintf("arn:aws:s3:::%s", bucket)
	}

	// ALB/ELB origin: contains .elb.amazonaws.com or .elasticloadbalancing.
	if strings.Contains(domain, ".elb.amazonaws.com") || strings.Contains(domain, ".elasticloadbalancing.") {
		return domain // ELB DNS name — the ELBv2 collector uses these as identifiers
	}

	// API Gateway origin: <id>.execute-api.<region>.amazonaws.com
	if strings.Contains(domain, ".execute-api.") {
		return domain // API Gateway DNS name
	}

	return ""
}

// cloudfrontDistributionMetadata extracts discriminating fields from a
// CloudFront distribution summary.
func cloudfrontDistributionMetadata(d cftypes.DistributionSummary) map[string]string {
	m := make(map[string]string, 3)
	if d.Enabled != nil {
		m["enabled"] = fmt.Sprintf("%t", awssdk.ToBool(d.Enabled))
	}
	if s := awssdk.ToString(d.Status); s != "" {
		m["status"] = s
	}
	if pc := string(d.PriceClass); pc != "" {
		m["price_class"] = pc
	}
	return m
}
