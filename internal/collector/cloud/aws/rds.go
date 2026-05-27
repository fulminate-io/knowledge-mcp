// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type rdsCollector struct {
	client    *rds.Client
	region    string
	accountID string
}

func newRDSCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &rdsCollector{
		client:    rds.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *rdsCollector) Name() string { return "rds" }

func (c *rdsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := rds.NewDescribeDBInstancesPaginator(c.client, &rds.DescribeDBInstancesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("rds: describe db instances: %w", err)
		}

		for _, instance := range page.DBInstances {
			content, err := json.Marshal(instance)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("rds: marshal: %w", err)
			}

			// RDS provides the ARN directly.
			dbARN := awssdk.ToString(instance.DBInstanceArn)
			dbName := awssdk.ToString(instance.DBInstanceIdentifier)

			resources = append(resources, cloud.ResourceSpec{
				ID:           dbARN,
				Name:         dbName,
				ResourceType: "rds-instance",
				Region:       c.region,
				Content:      content,
				Metadata:     rdsInstanceMetadata(instance),
			})

			edges = append(edges, c.instanceEdges(dbARN, instance)...)
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// instanceEdges extracts all edges for a single RDS instance: subnets, security groups, and IAM roles.
func (c *rdsCollector) instanceEdges(dbARN string, instance rdstypes.DBInstance) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// RDS → Subnets (via DBSubnetGroup)
	if instance.DBSubnetGroup != nil {
		for _, subnet := range instance.DBSubnetGroup.Subnets {
			if subnet.SubnetIdentifier != nil {
				subnetARN := ec2ARN(c.region, c.accountID, "subnet", awssdk.ToString(subnet.SubnetIdentifier))
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     dbARN,
					TargetID:     subnetARN,
					Relationship: kgtypes.EdgeUsesSubnet,
				})
			}
		}
	}

	// RDS → Security Groups
	for _, sg := range instance.VpcSecurityGroups {
		if sg.VpcSecurityGroupId != nil {
			sgARN := ec2ARN(c.region, c.accountID, "security-group", awssdk.ToString(sg.VpcSecurityGroupId))
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     dbARN,
				TargetID:     sgARN,
				Relationship: kgtypes.EdgeUsesSecurityGroup,
			})
		}
	}

	// RDS → KMS key (server-side encryption)
	if instance.KmsKeyId != nil {
		kmsARN := resolveKMSKeyARN(awssdk.ToString(instance.KmsKeyId), c.region, c.accountID)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     dbARN,
			TargetID:     kmsARN,
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}

	// RDS → IAM Roles (IAM database authentication, S3 integration, etc.)
	for _, role := range instance.AssociatedRoles {
		if role.RoleArn != nil {
			meta := map[string]string{"role_source": "associated_role"}
			if role.FeatureName != nil {
				meta["feature_name"] = *role.FeatureName
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     dbARN,
				TargetID:     awssdk.ToString(role.RoleArn),
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     meta,
			})
		}
	}

	// RDS read-replica chain. ReadReplicaSourceDBInstanceIdentifier links a
	// replica back to its primary; ReadReplicaDBInstanceIdentifiers lists the
	// replicas hanging off the primary. Cross-region replicas land here too —
	// the source identifier may be either a bare DBInstanceIdentifier (same
	// region) or a full ARN (cross-region). Resolve to ARN form so traversal
	// from primary to replica works whether or not the cross-region peer was
	// collected by another region's run.
	if src := awssdk.ToString(instance.ReadReplicaSourceDBInstanceIdentifier); src != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     dbARN,
			TargetID:     c.rdsInstanceARN(src),
			Relationship: kgtypes.EdgeReplicatesTo,
			Metadata:     map[string]string{"role": "replica"},
		})
	}
	for _, replica := range instance.ReadReplicaDBInstanceIdentifiers {
		if replica == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     dbARN,
			TargetID:     c.rdsInstanceARN(replica),
			Relationship: kgtypes.EdgeReplicatesTo,
			Metadata:     map[string]string{"role": "primary"},
		})
	}

	return edges
}

// rdsInstanceARN normalizes a read-replica identifier to an ARN. Already-ARN
// strings (cross-region replicas) pass through unchanged; bare identifiers
// are scoped to the local collector's region/account.
func (c *rdsCollector) rdsInstanceARN(idOrARN string) string {
	if strings.HasPrefix(idOrARN, "arn:") {
		return idOrARN
	}
	return fmt.Sprintf("arn:aws:rds:%s:%s:db:%s", c.region, c.accountID, idOrARN)
}

// rdsInstanceMetadata extracts discriminating fields from an RDS DB instance.
func rdsInstanceMetadata(d rdstypes.DBInstance) map[string]string {
	m := make(map[string]string, 5)
	if e := awssdk.ToString(d.Engine); e != "" {
		m["engine"] = e
	}
	if v := awssdk.ToString(d.EngineVersion); v != "" {
		m["engine_version"] = v
	}
	if c := awssdk.ToString(d.DBInstanceClass); c != "" {
		m["instance_class"] = c
	}
	if s := awssdk.ToString(d.DBInstanceStatus); s != "" {
		m["status"] = s
	}
	if d.MultiAZ != nil {
		m["multi_az"] = fmt.Sprintf("%t", awssdk.ToBool(d.MultiAZ))
	}
	return m
}
