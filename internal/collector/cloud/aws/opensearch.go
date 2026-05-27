// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	opensearchtypes "github.com/aws/aws-sdk-go-v2/service/opensearch/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type opensearchCollector struct {
	client    *opensearch.Client
	region    string
	accountID string
}

func newOpenSearchCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &opensearchCollector{
		client:    opensearch.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *opensearchCollector) Name() string { return "opensearch" }

func (c *opensearchCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	names, err := c.listOpenSearchDomains(ctx)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}
	if len(names) == 0 {
		return cloud.SubCollectorResult{}, nil
	}

	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)
	for _, chunk := range chunkNames(names, 5) {
		chunkResources, chunkEdges, err := c.describeOpenSearchDomains(ctx, chunk)
		if err != nil {
			return cloud.SubCollectorResult{}, err
		}
		resources = append(resources, chunkResources...)
		edges = append(edges, chunkEdges...)
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// listOpenSearchDomains returns the filtered set of domain names to
// describe. Empty-string names are dropped so DescribeDomains never sees
// an invalid chunk.
func (c *opensearchCollector) listOpenSearchDomains(ctx context.Context) ([]string, error) {
	list, err := c.client.ListDomainNames(ctx, &opensearch.ListDomainNamesInput{})
	if err != nil {
		return nil, fmt.Errorf("opensearch: list domain names: %w", err)
	}
	if len(list.DomainNames) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(list.DomainNames))
	for _, d := range list.DomainNames {
		if d.DomainName != nil && *d.DomainName != "" {
			names = append(names, *d.DomainName)
		}
	}
	return names, nil
}

// describeOpenSearchDomains invokes DescribeDomains on a single chunk of
// names and converts every returned domain into the resource + edge pair
// buildOpenSearchNode produces.
func (c *opensearchCollector) describeOpenSearchDomains(ctx context.Context, chunk []string) ([]cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	desc, err := c.client.DescribeDomains(ctx, &opensearch.DescribeDomainsInput{
		DomainNames: chunk,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("opensearch: describe domains: %w", err)
	}
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)
	for _, domain := range desc.DomainStatusList {
		res, domainEdges, err := c.buildOpenSearchNode(domain)
		if err != nil {
			return nil, nil, err
		}
		if res == nil {
			continue
		}
		resources = append(resources, *res)
		edges = append(edges, domainEdges...)
	}
	return resources, edges, nil
}

// buildOpenSearchNode converts one OpenSearch DomainStatus into the
// resource spec the collector publishes plus the VPC security-group and
// subnet edges implied by VPCOptions. Returns (nil, nil, nil) when the
// domain carries no ARN (unusable).
func (c *opensearchCollector) buildOpenSearchNode(domain opensearchtypes.DomainStatus) (*cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	content, err := json.Marshal(domain)
	if err != nil {
		return nil, nil, fmt.Errorf("opensearch: marshal: %w", err)
	}
	domainARN := awssdk.ToString(domain.ARN)
	if domainARN == "" {
		return nil, nil, nil
	}
	res := cloud.ResourceSpec{
		ID:           domainARN,
		Name:         awssdk.ToString(domain.DomainName),
		ResourceType: "opensearch-domain",
		Region:       c.region,
		Content:      content,
		Metadata:     opensearchDomainMetadata(domain),
	}
	var edges []cloud.EdgeSpec

	// OpenSearch → KMS key (encryption at rest)
	if domain.EncryptionAtRestOptions != nil && domain.EncryptionAtRestOptions.KmsKeyId != nil {
		kmsARN := resolveKMSKeyARN(awssdk.ToString(domain.EncryptionAtRestOptions.KmsKeyId), c.region, c.accountID)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     domainARN,
			TargetID:     kmsARN,
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}

	if domain.VPCOptions != nil {
		for _, sgID := range domain.VPCOptions.SecurityGroupIds {
			if sgID == "" {
				continue
			}
			sgARN := ec2ARN(c.region, c.accountID, "security-group", sgID)
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     domainARN,
				TargetID:     sgARN,
				Relationship: kgtypes.EdgeUsesSecurityGroup,
			})
		}
		for _, subnetID := range domain.VPCOptions.SubnetIds {
			if subnetID == "" {
				continue
			}
			subnetARN := ec2ARN(c.region, c.accountID, "subnet", subnetID)
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     domainARN,
				TargetID:     subnetARN,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
	}
	return &res, edges, nil
}

// chunkNames splits a slice into fixed-size chunks. Used to obey the
// DescribeDomains 5-name limit.
func chunkNames(names []string, size int) [][]string {
	if size <= 0 {
		size = 5
	}
	var out [][]string
	for i := 0; i < len(names); i += size {
		end := min(i+size, len(names))
		out = append(out, names[i:end])
	}
	return out
}

// opensearchDomainMetadata extracts discriminating fields from a domain.
func opensearchDomainMetadata(d opensearchtypes.DomainStatus) map[string]string {
	m := make(map[string]string, 3)
	if v := awssdk.ToString(d.EngineVersion); v != "" {
		m["engine_version"] = v
	}
	if d.ClusterConfig != nil {
		if t := string(d.ClusterConfig.InstanceType); t != "" {
			m["instance_type"] = t
		}
		if d.ClusterConfig.InstanceCount != nil {
			m["instance_count"] = fmt.Sprintf("%d", awssdk.ToInt32(d.ClusterConfig.InstanceCount))
		}
	}
	return m
}
