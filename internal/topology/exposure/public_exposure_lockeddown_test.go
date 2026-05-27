// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// public_exposure_lockeddown_test.go carries the shared negative Phase 7
// test — ALL three public-exposure analyzers must emit zero findings on
// a fixture with no public entry points, no open SG rules, no public S3
// buckets, and no K8s LoadBalancer Services.

// TestPublicExposure_E2E_LockedDown builds a locked-down AWS + K8s
// fixture (internal-only ALB, locked S3, private RDS, ClusterIP K8s
// Service, no IRSA annotations) and asserts all three analyzers return
// zero findings. This pins the "no false positives on a hardened
// account" contract that's the single most important property of a
// public-exposure analyzer family: a noisy analyzer is worse than no
// analyzer because operators train themselves to ignore the alerts.
func TestPublicExposure_E2E_LockedDown(t *testing.T) {
	fx := newExposureFixture(t)

	const (
		// AWS locked-down resources.
		internalLBID = "arn:aws:elbv2:us-east-1:000000000001:loadbalancer/internal"
		rdsID        = "arn:aws:rds:us-east-1:000000000001:db:private-db"
		bucketID     = "arn:aws:s3:::locked-down-bucket"
		// K8s cluster-internal resources.
		clusterIPSvc  = "prod/Service/api-internal"
		privatePodID  = "prod/Pod/api-worker"
		privateSecret = "prod/Secret/internal-keys"
	)

	// Internal-only ALB (Scheme=internal ≠ internet-facing, so awsELBv2SeedRule
	// returns nil).
	fx.addNode(internalLBID, "elbv2-loadbalancer", map[string]any{
		"Scheme": "internal",
	}, nil)

	// Private RDS — PubliclyAccessible=false so awsRDSSeedRule returns nil.
	// (But awsRDSInstanceSensitive still flags it as sensitive, which is
	// what we want: a sensitive terminal with no path to it from public is
	// a legitimately locked-down configuration.)
	fx.addRDSInstance(rdsID)

	// Fully locked-down S3 bucket — PAB all-true, no policy, no ACL
	// grants. awsS3SeedRule returns nil for this configuration.
	fx.addLockedDownS3Bucket(bucketID)

	// ClusterIP Service — k8sServiceSeedRule ignores anything that is not
	// of type LoadBalancer.
	fx.addK8sClusterIPService(clusterIPSvc, "prod")

	// Private pod + secret — not sensitive terminals by themselves in a
	// zero-seed graph; no walker run even reaches them because there are
	// no seeds to start from.
	fx.addK8sPod(privatePodID, "prod")
	fx.addK8sSecret(privateSecret, "prod")

	// Wire a few benign structural edges to prove the walker wouldn't
	// crash even if it had seeds — it just has nothing to walk from.
	fx.link(clusterIPSvc, privatePodID, kgtypes.EdgeSelects)
	fx.link(privatePodID, privateSecret, kgtypes.EdgeMountsSecret)

	// All three analyzers must emit zero findings.
	awsFindings := fx.runAWSAnalyzer()
	require.Emptyf(t, awsFindings,
		"aws_public_exposure must emit zero findings on locked-down fixture, got %+v", awsFindings)

	k8sFindings := fx.runK8sAnalyzer()
	require.Emptyf(t, k8sFindings,
		"k8s_public_exposure must emit zero findings on locked-down fixture, got %+v", k8sFindings)

	unifiedFindings := fx.runUnifiedAnalyzer()
	require.Emptyf(t, unifiedFindings,
		"unified_public_exposure must emit zero findings on locked-down fixture, got %+v", unifiedFindings)
}
