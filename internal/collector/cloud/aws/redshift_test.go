// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	redshifttypes "github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const (
	testRegion    = "us-east-1"
	testAccountID = "111111111111"
	testClusterID = "my-warehouse"
)

func testClusterARN() string {
	return redshiftClusterARN(testRegion, testAccountID, testClusterID)
}

func newTestRedshiftCollector() *redshiftCollector {
	return &redshiftCollector{region: testRegion, accountID: testAccountID}
}

func TestClusterEdges_VPCNetwork(t *testing.T) {
	c := newTestRedshiftCollector()
	clusterARN := testClusterARN()

	t.Run("emits EdgeUsesNetwork when VpcId set", func(t *testing.T) {
		cluster := redshifttypes.Cluster{
			ClusterIdentifier: awssdk.String(testClusterID),
			VpcId:             awssdk.String("vpc-0123456789abcdef0"),
		}
		edges := c.clusterEdges(context.Background(), clusterARN, cluster, map[string][]string{})

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeUsesNetwork {
				assert.Equal(t, clusterARN, e.SourceID)
				assert.Equal(t, "arn:aws:ec2:us-east-1:111111111111:vpc/vpc-0123456789abcdef0", e.TargetID)
				found = true
			}
		}
		require.True(t, found, "expected EdgeUsesNetwork edge")
	})

	t.Run("no EdgeUsesNetwork when VpcId nil", func(t *testing.T) {
		cluster := redshifttypes.Cluster{ClusterIdentifier: awssdk.String(testClusterID)}
		edges := c.clusterEdges(context.Background(), clusterARN, cluster, map[string][]string{})
		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeUsesNetwork, e.Relationship)
		}
	})
}

func TestClusterEdges_Subnets(t *testing.T) {
	c := newTestRedshiftCollector()
	clusterARN := testClusterARN()

	t.Run("emits EdgeUsesSubnet for each resolved subnet", func(t *testing.T) {
		// Pre-populate the cache so resolveSubnetGroup doesn't call AWS.
		cache := map[string][]string{
			"my-subnet-group": {"subnet-aaaa", "subnet-bbbb"},
		}
		cluster := redshifttypes.Cluster{
			ClusterIdentifier:      awssdk.String(testClusterID),
			ClusterSubnetGroupName: awssdk.String("my-subnet-group"),
		}
		edges := c.clusterEdges(context.Background(), clusterARN, cluster, cache)

		var subnets []string
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeUsesSubnet {
				assert.Equal(t, clusterARN, e.SourceID)
				subnets = append(subnets, e.TargetID)
			}
		}
		assert.ElementsMatch(t, []string{
			"arn:aws:ec2:us-east-1:111111111111:subnet/subnet-aaaa",
			"arn:aws:ec2:us-east-1:111111111111:subnet/subnet-bbbb",
		}, subnets)
	})

	t.Run("no EdgeUsesSubnet when ClusterSubnetGroupName nil", func(t *testing.T) {
		cluster := redshifttypes.Cluster{ClusterIdentifier: awssdk.String(testClusterID)}
		edges := c.clusterEdges(context.Background(), clusterARN, cluster, map[string][]string{})
		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeUsesSubnet, e.Relationship)
		}
	})
}

func TestClusterEdges_SecurityGroups(t *testing.T) {
	c := newTestRedshiftCollector()
	clusterARN := testClusterARN()

	t.Run("emits EdgeUsesSecurityGroup for each VPC SG", func(t *testing.T) {
		cluster := redshifttypes.Cluster{
			ClusterIdentifier: awssdk.String(testClusterID),
			VpcSecurityGroups: []redshifttypes.VpcSecurityGroupMembership{
				{VpcSecurityGroupId: awssdk.String("sg-1111")},
				{VpcSecurityGroupId: awssdk.String("sg-2222")},
			},
		}
		edges := c.clusterEdges(context.Background(), clusterARN, cluster, map[string][]string{})

		var sgs []string
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeUsesSecurityGroup {
				assert.Equal(t, clusterARN, e.SourceID)
				sgs = append(sgs, e.TargetID)
			}
		}
		assert.ElementsMatch(t, []string{
			"arn:aws:ec2:us-east-1:111111111111:security-group/sg-1111",
			"arn:aws:ec2:us-east-1:111111111111:security-group/sg-2222",
		}, sgs)
	})
}

func TestClusterEdges_IAMRoles(t *testing.T) {
	c := newTestRedshiftCollector()
	clusterARN := testClusterARN()
	roleARN := "arn:aws:iam::111111111111:role/RedshiftCopy"

	t.Run("emits EdgeAssumesRole with role_source metadata", func(t *testing.T) {
		cluster := redshifttypes.Cluster{
			ClusterIdentifier: awssdk.String(testClusterID),
			IamRoles: []redshifttypes.ClusterIamRole{
				{IamRoleArn: awssdk.String(roleARN), ApplyStatus: awssdk.String("in-sync")},
			},
		}
		edges := c.clusterEdges(context.Background(), clusterARN, cluster, map[string][]string{})

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeAssumesRole {
				assert.Equal(t, clusterARN, e.SourceID)
				assert.Equal(t, roleARN, e.TargetID)
				assert.Equal(t, "cluster_iam_role", e.Metadata["role_source"])
				assert.Equal(t, "in-sync", e.Metadata["apply_status"])
				found = true
			}
		}
		require.True(t, found, "expected EdgeAssumesRole edge")
	})

	t.Run("no EdgeAssumesRole when IamRoles empty", func(t *testing.T) {
		cluster := redshifttypes.Cluster{ClusterIdentifier: awssdk.String(testClusterID)}
		edges := c.clusterEdges(context.Background(), clusterARN, cluster, map[string][]string{})
		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeAssumesRole, e.Relationship)
		}
	})
}

func TestClusterEdges_KMSEncryption(t *testing.T) {
	c := newTestRedshiftCollector()
	clusterARN := testClusterARN()
	kmsARN := "arn:aws:kms:us-east-1:111111111111:key/test-key-id"

	t.Run("emits EdgeEncryptsWith when KmsKeyId set", func(t *testing.T) {
		cluster := redshifttypes.Cluster{
			ClusterIdentifier: awssdk.String(testClusterID),
			KmsKeyId:          awssdk.String(kmsARN),
		}
		edges := c.clusterEdges(context.Background(), clusterARN, cluster, map[string][]string{})

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, clusterARN, e.SourceID)
				assert.Equal(t, kmsARN, e.TargetID)
				found = true
			}
		}
		require.True(t, found, "expected EdgeEncryptsWith edge")
	})

	t.Run("no EdgeEncryptsWith when KmsKeyId nil", func(t *testing.T) {
		cluster := redshifttypes.Cluster{ClusterIdentifier: awssdk.String(testClusterID)}
		edges := c.clusterEdges(context.Background(), clusterARN, cluster, map[string][]string{})
		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeEncryptsWith, e.Relationship)
		}
	})
}

func TestClusterEdges_EmptyCluster(t *testing.T) {
	c := newTestRedshiftCollector()
	cluster := redshifttypes.Cluster{ClusterIdentifier: awssdk.String(testClusterID)}
	edges := c.clusterEdges(context.Background(), testClusterARN(), cluster, map[string][]string{})
	assert.Empty(t, edges, "empty cluster should emit no edges")
}

func TestRedshiftClusterARN(t *testing.T) {
	got := redshiftClusterARN("us-west-2", "222222222222", "analytics")
	assert.Equal(t, "arn:aws:redshift:us-west-2:222222222222:cluster:analytics", got)
}
