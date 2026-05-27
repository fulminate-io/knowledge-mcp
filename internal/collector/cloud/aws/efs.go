// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type efsCollector struct {
	client    *efs.Client
	region    string
	accountID string
}

func newEFSCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &efsCollector{
		client:    efs.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *efsCollector) Name() string { return "efs" }

func (c *efsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	// 1. Paginate file systems. Each EFS file system gets one node.
	fsPaginator := efs.NewDescribeFileSystemsPaginator(c.client, &efs.DescribeFileSystemsInput{})
	for fsPaginator.HasMorePages() {
		page, err := fsPaginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("efs: describe file systems: %w", err)
		}
		for _, fs := range page.FileSystems {
			content, err := json.Marshal(fs)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("efs: marshal file system: %w", err)
			}
			fsARN := awssdk.ToString(fs.FileSystemArn)
			fsID := awssdk.ToString(fs.FileSystemId)
			if fsARN == "" || fsID == "" {
				continue
			}
			resources = append(resources, cloud.ResourceSpec{
				ID:           fsARN,
				Name:         fsID,
				ResourceType: "efs-filesystem",
				Region:       c.region,
				Content:      content,
				Metadata:     efsFileSystemMetadata(fs),
			})

			// EFS → KMS key (encryption at rest)
			if fs.Encrypted != nil && *fs.Encrypted && fs.KmsKeyId != nil {
				kmsARN := resolveKMSKeyARN(awssdk.ToString(fs.KmsKeyId), c.region, c.accountID)
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     fsARN,
					TargetID:     kmsARN,
					Relationship: kgtypes.EdgeEncryptsWith,
				})
			}

			// 2. List mount targets per file system.
			mtEdges, err := c.mountTargetEdges(ctx, fsARN, fsID)
			if err != nil {
				return cloud.SubCollectorResult{}, err
			}
			edges = append(edges, mtEdges...)
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// mountTargetEdges enumerates mount targets for a single file system
// and emits EdgeUsesSubnet + EdgeUsesSecurityGroup edges. Mount-target
// security groups come from a separate DescribeMountTargetSecurityGroups
// call — the MountTargetDescription struct itself only carries the
// subnet and network interface.
func (c *efsCollector) mountTargetEdges(ctx context.Context, fsARN, fsID string) ([]cloud.EdgeSpec, error) {
	var edges []cloud.EdgeSpec

	paginator := efs.NewDescribeMountTargetsPaginator(c.client, &efs.DescribeMountTargetsInput{
		FileSystemId: awssdk.String(fsID),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("efs: describe mount targets for %s: %w", fsID, err)
		}
		for _, mt := range page.MountTargets {
			if mt.MountTargetId == nil {
				continue
			}
			if mt.SubnetId != nil {
				subnetARN := ec2ARN(c.region, c.accountID, "subnet", *mt.SubnetId)
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     fsARN,
					TargetID:     subnetARN,
					Relationship: kgtypes.EdgeUsesSubnet,
				})
			}

			sgs, err := c.client.DescribeMountTargetSecurityGroups(ctx, &efs.DescribeMountTargetSecurityGroupsInput{
				MountTargetId: mt.MountTargetId,
			})
			if err != nil {
				// Some mount targets may transiently fail — skip.
				continue
			}
			for _, sgID := range sgs.SecurityGroups {
				if sgID == "" {
					continue
				}
				sgARN := ec2ARN(c.region, c.accountID, "security-group", sgID)
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     fsARN,
					TargetID:     sgARN,
					Relationship: kgtypes.EdgeUsesSecurityGroup,
				})
			}
		}
	}
	return edges, nil
}

// efsFileSystemMetadata extracts discriminating fields from an EFS file system.
func efsFileSystemMetadata(fs efstypes.FileSystemDescription) map[string]string {
	m := make(map[string]string, 4)
	if fs.Encrypted != nil {
		m["encrypted"] = fmt.Sprintf("%t", awssdk.ToBool(fs.Encrypted))
	}
	if pm := string(fs.PerformanceMode); pm != "" {
		m["performance_mode"] = pm
	}
	if tm := string(fs.ThroughputMode); tm != "" {
		m["throughput_mode"] = tm
	}
	if ls := string(fs.LifeCycleState); ls != "" {
		m["life_cycle_state"] = ls
	}
	return m
}
