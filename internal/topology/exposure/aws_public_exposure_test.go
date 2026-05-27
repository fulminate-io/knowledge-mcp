// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// aws_public_exposure_test.go runs the AWSPublicExposureAnalyzer end-to-
// end against a minimal cloud fixture and asserts the emitted findings
// look right: algorithm name, severity, evidence ordering, and the
// presence of the walker-discovered terminal in each.

const awsPEAccount = "aws-pe-smoke"

// TestAWSPublicExposureAnalyzer_EndToEnd wires a public ALB to a security
// group chain that terminates at an RDS instance and verifies the
// analyzer surfaces at least one finding with the expected shape.
func TestAWSPublicExposureAnalyzer_EndToEnd(t *testing.T) {
	fx := newCloudFixture(t)
	fx.account(awsPEAccount)

	lb, err := json.Marshal(map[string]any{"Scheme": "internet-facing"})
	require.NoError(t, err)
	sgFront, err := json.Marshal(map[string]any{})
	require.NoError(t, err)
	sgBack, err := json.Marshal(map[string]any{})
	require.NoError(t, err)
	rds, err := json.Marshal(map[string]any{"PubliclyAccessible": false})
	require.NoError(t, err)

	fx.AddCloudResourceWithContent(awsPEAccount, "arn:alb", "arn:alb", "elbv2-loadbalancer", string(lb), nil)
	fx.AddCloudResourceWithContent(awsPEAccount, "arn:sg-front", "arn:sg-front", "security-group", string(sgFront), nil)
	fx.AddCloudResourceWithContent(awsPEAccount, "arn:sg-back", "arn:sg-back", "security-group", string(sgBack), nil)
	fx.AddCloudResourceWithContent(awsPEAccount, "arn:rds", "arn:rds", "rds-instance", string(rds), nil)

	fx.AddEdge(awsPEAccount, "arn:alb", "arn:sg-front", kgtypes.EdgeUsesSecurityGroup)
	fx.AddEdge(awsPEAccount, "arn:sg-front", "arn:sg-back", kgtypes.EdgeAllowsEgressTo)
	fx.AddEdge(awsPEAccount, "arn:sg-back", "arn:rds", kgtypes.EdgeUsesSecurityGroup)

	req := Request{
		Caller: fx,
		Graph:  kgtypes.GraphCloud,
		Name:   awsPEAccount,
	}
	findings, err := AWSPublicExposureAnalyzer{}.Run(newTestCtx(t), req)
	require.NoError(t, err)
	require.NotEmpty(t, findings, "analyzer must emit at least one finding")

	// Every finding must carry the correct algorithm and point at the
	// ALB as the seed (Evidence[0]).
	for _, f := range findings {
		require.Equal(t, "aws_public_exposure", f.Algorithm)
		require.NotEmpty(t, f.Evidence)
		require.Equal(t, "arn:alb", f.Evidence[0])
	}

	// At least one finding should terminate at the RDS instance.
	var found bool
	for _, f := range findings {
		if f.Evidence[len(f.Evidence)-1] == "arn:rds" {
			found = true
			require.Contains(t, f.Summary, "internet-facing load balancer")
			require.Contains(t, f.Summary, "relational database")
			break
		}
	}
	require.True(t, found, "analyzer must surface the ALB → RDS path")
}

// TestAWSPublicExposureAnalyzer_WrongGraphReturnsNil verifies the
// analyzer returns (nil, nil) when invoked on a non-cloud graph — it
// must stay harmless under dream-topology dispatch against unrelated
// graphs.
func TestAWSPublicExposureAnalyzer_WrongGraphReturnsNil(t *testing.T) {
	fx := newCloudFixture(t)
	req := Request{Caller: fx, Graph: kgtypes.GraphKnowledge, Name: "default"}
	findings, err := AWSPublicExposureAnalyzer{}.Run(newTestCtx(t), req)
	require.NoError(t, err)
	require.Nil(t, findings)
}

// TestAWSPublicExposureAnalyzer_EmptyAccountErrors verifies a req with
// no account name returns an error rather than silently walking the
// wrong graph.
func TestAWSPublicExposureAnalyzer_EmptyAccountErrors(t *testing.T) {
	fx := newCloudFixture(t)
	req := Request{Caller: fx, Graph: kgtypes.GraphCloud, Name: ""}
	_, err := AWSPublicExposureAnalyzer{}.Run(newTestCtx(t), req)
	require.Error(t, err)
}

// TestAWSPublicExposureAnalyzer_E2E_AlbEc2Admin is the Phase 7 fixture-
// driven end-to-end test for the canonical "internet-facing ALB → SG →
// EC2 → IAM admin role" scenario. It verifies:
//
//  1. The analyzer produces at least one finding.
//  2. Every finding carries algorithm="aws_public_exposure".
//  3. At least one finding reaches the IAM admin role terminal at critical
//     severity, traversing the ALB → SG → EC2 → IAM path.
//  4. No K8s node IDs appear in the hop list (AWS-only analyzer must not
//     cross into K8s territory — that path belongs to the unified
//     analyzer).
func TestAWSPublicExposureAnalyzer_E2E_AlbEc2Admin(t *testing.T) {
	fx := newExposureFixture(t)

	const (
		albID  = "arn:aws:elbv2:us-east-1:000000000001:loadbalancer/app/test/123"
		sgID   = "arn:aws:ec2:us-east-1:000000000001:security-group/sg-front"
		ec2ID  = "arn:aws:ec2:us-east-1:000000000001:instance/i-app"
		roleID = "arn:aws:iam::000000000001:role/app-admin"
	)

	fx.addInternetFacingALB(albID)
	fx.addSecurityGroup(sgID)
	fx.addEC2Instance(ec2ID)
	fx.addIAMRoleWithAdminFinding(roleID)

	// Wire the hop chain: ALB -UsesSG-> sg, sg -AllowsIngressFrom-> ec2,
	// ec2 -AssumesRole-> iam-role. EdgeAssumesRole is in
	// awsPublicExposureEdgeTypes so the walker will follow it.
	fx.link(albID, sgID, kgtypes.EdgeUsesSecurityGroup)
	fx.link(sgID, ec2ID, kgtypes.EdgeAllowsIngressFrom)
	fx.link(ec2ID, roleID, kgtypes.EdgeAssumesRole)

	findings := fx.runAWSAnalyzer()
	require.NotEmpty(t, findings, "analyzer must emit at least one finding")

	// Every finding must carry the AWS analyzer's algorithm.
	for _, f := range findings {
		require.Equal(t, "aws_public_exposure", f.Algorithm)
		require.NotEmpty(t, f.Evidence)
	}

	// At least one finding must reach the IAM admin role terminal with
	// critical severity and a hop list that traverses ALB → SG → EC2 → IAM.
	var matched int
	for _, f := range findings {
		terminal := f.Evidence[len(f.Evidence)-1]
		if terminal != roleID {
			continue
		}
		matched++
		require.Equal(t, SeverityCritical, f.Severity,
			"IAM-admin terminal must produce critical severity, got %v", f.Severity)
		// Evidence is the ordered hop list — seed first, terminal last.
		require.Equal(t, albID, f.Evidence[0], "seed must be the ALB")
		nodeSet := map[string]bool{}
		for _, id := range f.Evidence {
			nodeSet[id] = true
		}
		require.True(t, nodeSet[sgID], "hop list must include the SG")
		require.True(t, nodeSet[ec2ID], "hop list must include the EC2 instance")
		// AWS-only analyzer must not leak any K8s node IDs into the hop
		// list. K8s IDs use the "/Pod/", "/Service/", etc. separator.
		for _, id := range f.Evidence {
			require.NotContains(t, id, "/Pod/", "AWS analyzer must not walk into K8s pods")
			require.NotContains(t, id, "/Service/", "AWS analyzer must not walk into K8s services")
			require.NotContains(t, id, "/ServiceAccount/", "AWS analyzer must not walk into K8s SAs")
		}
	}
	require.GreaterOrEqual(t, matched, 1, "analyzer must emit at least one ALB → IAM admin finding")
}

// TestAWSPublicExposureAnalyzer_E2E_S3WideIam exercises the public-S3 +
// wide-IAM terminal scenario. Two buckets both reach an IAM admin role
// via EdgeAssumesRole, producing at least two distinct findings. This is
// the Phase 7 S3-seed acceptance test.
func TestAWSPublicExposureAnalyzer_E2E_S3WideIam(t *testing.T) {
	fx := newExposureFixture(t)

	const (
		bucketAID = "arn:aws:s3:::public-bucket-acl"
		bucketBID = "arn:aws:s3:::public-bucket-policy"
		roleID    = "arn:aws:iam::000000000001:role/s3-wildcard-admin"
	)

	fx.addPublicS3Bucket(bucketAID)
	// Second public bucket via public bucket policy.
	fx.addNode(bucketBID, "s3-bucket", map[string]any{
		"bucket_policy_status": map[string]any{"is_public": true},
	}, nil)
	fx.addIAMRoleWithAdminFinding(roleID)

	// Each bucket has an EdgeAssumesRole edge to the admin role. Wide
	// IAM permissions (s3:*) are encoded structurally here as "every
	// bucket can reach the admin role". The walker emits one finding
	// per distinct seed → terminal path.
	fx.link(bucketAID, roleID, kgtypes.EdgeAssumesRole)
	fx.link(bucketBID, roleID, kgtypes.EdgeAssumesRole)

	findings := fx.runAWSAnalyzer()
	require.NotEmpty(t, findings, "analyzer must emit at least one finding for wide-IAM S3 scenario")

	// Count distinct (seed, terminal) tuples to ensure we have at least
	// two genuinely distinct findings — the "at least 2" Phase 7 criterion.
	type seedTerm struct{ seed, term string }
	seen := map[seedTerm]bool{}
	for _, f := range findings {
		require.Equal(t, "aws_public_exposure", f.Algorithm)
		require.NotEmpty(t, f.Evidence)
		key := seedTerm{
			seed: f.Evidence[0],
			term: f.Evidence[len(f.Evidence)-1],
		}
		seen[key] = true
	}
	require.GreaterOrEqualf(t, len(seen), 2,
		"expected at least 2 distinct seed → terminal findings, got %d: %+v", len(seen), seen)

	// Both buckets must appear as seeds in at least one finding each.
	var sawBucketA, sawBucketB bool
	for k := range seen {
		if k.seed == bucketAID {
			sawBucketA = true
		}
		if k.seed == bucketBID {
			sawBucketB = true
		}
	}
	require.True(t, sawBucketA, "public S3 bucket A (ACL) must produce at least one finding")
	require.True(t, sawBucketB, "public S3 bucket B (policy) must produce at least one finding")
}
