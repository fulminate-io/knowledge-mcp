// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type elbv2Collector struct {
	client    *elbv2.Client
	wafClient *wafv2.Client
	region    string
	accountID string
}

func newELBv2Collector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &elbv2Collector{
		client:    elbv2.NewFromConfig(cfg),
		wafClient: wafv2.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *elbv2Collector) Name() string { return "elbv2" }

func (c *elbv2Collector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	// Collect load balancers.
	lbPaginator := elbv2.NewDescribeLoadBalancersPaginator(c.client, &elbv2.DescribeLoadBalancersInput{})
	for lbPaginator.HasMorePages() {
		page, err := lbPaginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("elbv2: describe load balancers: %w", err)
		}

		for _, lb := range page.LoadBalancers {
			content, err := json.Marshal(lb)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("elbv2: marshal lb: %w", err)
			}

			// LB ARN comes directly from the SDK response.
			lbARN := awssdk.ToString(lb.LoadBalancerArn)
			lbName := awssdk.ToString(lb.LoadBalancerName)

			resources = append(resources, cloud.ResourceSpec{
				ID:           lbARN,
				Name:         lbName,
				ResourceType: "elbv2-loadbalancer",
				Region:       c.region,
				Content:      content,
				Metadata:     elbv2LoadBalancerMetadata(lb),
			})

			// LB → VPC
			if lb.VpcId != nil {
				vpcARN := ec2ARN(c.region, c.accountID, "vpc", awssdk.ToString(lb.VpcId))
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     lbARN,
					TargetID:     vpcARN,
					Relationship: kgtypes.EdgeUsesNetwork,
				})
			}

			// LB → Subnets (from AvailabilityZones)
			for _, az := range lb.AvailabilityZones {
				if az.SubnetId != nil {
					subnetARN := ec2ARN(c.region, c.accountID, "subnet", awssdk.ToString(az.SubnetId))
					edges = append(edges, cloud.EdgeSpec{
						SourceID:     lbARN,
						TargetID:     subnetARN,
						Relationship: kgtypes.EdgeUsesSubnet,
						Metadata: map[string]string{
							"availability_zone": awssdk.ToString(az.ZoneName),
						},
					})
				}
			}

			// LB → Security Groups
			for _, sgID := range lb.SecurityGroups {
				sgARN := ec2ARN(c.region, c.accountID, "security-group", sgID)
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     lbARN,
					TargetID:     sgARN,
					Relationship: kgtypes.EdgeUsesSecurityGroup,
				})
			}

			// LB → ACM Certificates (via listeners)
			edges = append(edges, c.collectListenerCerts(ctx, lbARN)...)

			// LB → WAF WebACL (best-effort)
			edges = append(edges, c.lookupWebACL(ctx, lbARN)...)
		}
	}

	// Collect target groups.
	tgResources, tgEdges, err := c.collectTargetGroups(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}
	resources = append(resources, tgResources...)
	edges = append(edges, tgEdges...)

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// collectListenerCerts calls DescribeListeners for a load balancer and returns
// USES_CERT edges for each ACM certificate found on HTTPS/TLS listeners.
func (c *elbv2Collector) collectListenerCerts(ctx context.Context, lbARN string) []cloud.EdgeSpec {
	var listeners []elbv2types.Listener

	paginator := elbv2.NewDescribeListenersPaginator(c.client, &elbv2.DescribeListenersInput{
		LoadBalancerArn: awssdk.String(lbARN),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			// Fail-open: listener access may be restricted.
			return nil
		}
		listeners = append(listeners, page.Listeners...)
	}
	return listenerCertEdges(lbARN, listeners)
}

// listenerCertEdges builds USES_CERT EdgeSpecs from elbv2 listener data.
// Deduplicates certificates that appear on multiple listeners.
func listenerCertEdges(lbARN string, listeners []elbv2types.Listener) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	seen := make(map[string]struct{})

	for _, listener := range listeners {
		for _, cert := range listener.Certificates {
			certARN := awssdk.ToString(cert.CertificateArn)
			if certARN == "" {
				continue
			}
			if _, ok := seen[certARN]; ok {
				continue
			}
			seen[certARN] = struct{}{}
			meta := map[string]string{}
			if listener.Port != nil {
				meta["listener_port"] = fmt.Sprintf("%d", *listener.Port)
			}
			if listener.Protocol != "" {
				meta["protocol"] = string(listener.Protocol)
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     lbARN,
				TargetID:     certARN,
				Relationship: kgtypes.EdgeUsesCert,
				Metadata:     meta,
			})
		}
	}
	return edges
}

func (c *elbv2Collector) collectTargetGroups(ctx context.Context) ([]cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	tgPaginator := elbv2.NewDescribeTargetGroupsPaginator(c.client, &elbv2.DescribeTargetGroupsInput{})
	for tgPaginator.HasMorePages() {
		page, err := tgPaginator.NextPage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("elbv2: describe target groups: %w", err)
		}

		for _, tg := range page.TargetGroups {
			content, err := json.Marshal(tg)
			if err != nil {
				return nil, nil, fmt.Errorf("elbv2: marshal target group: %w", err)
			}

			tgARN := awssdk.ToString(tg.TargetGroupArn)
			tgName := awssdk.ToString(tg.TargetGroupName)

			resources = append(resources, cloud.ResourceSpec{
				ID:           tgARN,
				Name:         tgName,
				ResourceType: "elbv2-targetgroup",
				Region:       c.region,
				Content:      content,
				Metadata:     elbv2TargetGroupMetadata(tg),
			})

			// Target group → Load Balancer (via LoadBalancerArns)
			meta := map[string]string{}
			if tg.Port != nil {
				meta["port"] = fmt.Sprintf("%d", *tg.Port)
			}
			if tg.Protocol != "" {
				meta["protocol"] = string(tg.Protocol)
			}
			if tg.TargetType != "" {
				meta["target_type"] = string(tg.TargetType)
			}
			for _, lbARN := range tg.LoadBalancerArns {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     lbARN,
					TargetID:     tgARN,
					Relationship: kgtypes.EdgeTargets,
					Metadata:     meta,
				})
			}

			// TG → registered targets (instances, lambdas, IPs).
			thEdges, err := collectTargetHealth(ctx, c.client, tgARN, tg.TargetType, c.region, c.accountID)
			if err != nil {
				// Fail-open: log and continue.
				slog.Warn("elbv2: collectTargetHealth failed", "tg", tgARN, "err", err)
			} else {
				edges = append(edges, thEdges...)
			}
		}
	}

	return resources, edges, nil
}

// elbv2LoadBalancerMetadata extracts discriminating fields from an ELBv2 LB.
func elbv2LoadBalancerMetadata(lb elbv2types.LoadBalancer) map[string]string {
	m := make(map[string]string, 3)
	if t := string(lb.Type); t != "" {
		m["type"] = t
	}
	if s := string(lb.Scheme); s != "" {
		m["scheme"] = s
	}
	if v := awssdk.ToString(lb.VpcId); v != "" {
		m["vpc_id"] = v
	}
	return m
}

// elbv2TargetGroupMetadata extracts discriminating fields from a target group.
func elbv2TargetGroupMetadata(tg elbv2types.TargetGroup) map[string]string {
	m := make(map[string]string, 4)
	if p := string(tg.Protocol); p != "" {
		m["protocol"] = p
	}
	if tg.Port != nil {
		m["port"] = fmt.Sprintf("%d", awssdk.ToInt32(tg.Port))
	}
	if t := string(tg.TargetType); t != "" {
		m["target_type"] = t
	}
	if v := awssdk.ToString(tg.VpcId); v != "" {
		m["vpc_id"] = v
	}
	return m
}
