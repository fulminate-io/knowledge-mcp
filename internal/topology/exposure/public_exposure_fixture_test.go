// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// public_exposure_fixture_test.go provides shared fixture helpers for the
// Phase 7 end-to-end tests covering AWSPublicExposureAnalyzer,
// K8sPublicExposureAnalyzer, and UnifiedPublicExposureAnalyzer.
//
// Each helper wraps the lower-level cloudFixture with public-exposure-
// specific sugar: creating cloud-resource nodes with the right resource_type
// metadata and JSON content envelopes matching what the collectors write,
// and plumbing through the persisted iam_escalation finding the walker
// consults via classifySensitive.
//
// DESIGN NOTES
//
// The fixture is intentionally single-account because the public-exposure
// analyzers dispatch one account at a time. Tests needing cross-account
// behavior should fall back to cloudFixture directly.
//
// Content envelopes are hand-built from anonymous maps so topology/ does
// not need to import cloud/aws or cloud/k8s. The map keys mirror exactly
// what the seed rules in public_exposure_seeds_aws.go / public_exposure_seeds_k8s.go
// expect to parse out of node.Content.

const peE2EAccount = "pe-e2e"

// exposureFixture is the shared fixture wrapper for the public-exposure
// E2E tests. Wraps a cloudFixture rooted at a single test account.
type exposureFixture struct {
	t  *testing.T
	fx *cloudFixture
}

// newExposureFixture returns a fresh exposureFixture with an empty
// peE2EAccount cloud graph ready to receive resources.
func newExposureFixture(t *testing.T) *exposureFixture {
	t.Helper()
	fx := newCloudFixture(t)
	fx.account(peE2EAccount)
	return &exposureFixture{t: t, fx: fx}
}

// caller returns the wire caller analyzers receive in Request.Caller.
func (f *exposureFixture) caller() foundation.GraphCaller { return f.fx }

// Account returns the fixture's single account name.
func (f *exposureFixture) Account() string { return peE2EAccount }

// link adds one directed edge in the fixture's account.
func (f *exposureFixture) link(fromID, toID string, edgeType kgtypes.EdgeType) {
	f.t.Helper()
	f.fx.AddEdge(peE2EAccount, fromID, toID, edgeType)
}

// addNode creates a cloud-resource node with the given id, resource_type,
// arbitrary JSON content, and optional metadata pairs. Used by the typed
// helpers below so each scenario file reads as a declarative recipe.
func (f *exposureFixture) addNode(id, resourceType string, content any, meta map[string]string) {
	f.t.Helper()
	body, err := json.Marshal(content)
	require.NoError(f.t, err)
	f.fx.AddCloudResourceWithContent(peE2EAccount, id, id, resourceType, string(body), meta)
}

// addInternetFacingALB creates an elbv2-loadbalancer node with
// Scheme="internet-facing", which the awsELBv2SeedRule recognizes as a
// tier-critical public entry (score 0.9).
func (f *exposureFixture) addInternetFacingALB(id string) {
	f.t.Helper()
	f.addNode(id, "elbv2-loadbalancer", map[string]any{
		"Scheme": "internet-facing",
	}, nil)
}

// addSecurityGroup creates a security-group node. The walker treats SGs
// as hop-through nodes; no content is needed to satisfy the walker.
func (f *exposureFixture) addSecurityGroup(id string) {
	f.t.Helper()
	f.addNode(id, "security-group", map[string]any{}, nil)
}

// addEC2Instance creates an ec2-instance node with no public IP. The
// walker treats this as a generic hop node — the awsEC2SeedRule only
// fires when PublicIpAddress is set, which we leave empty here by design
// (the EC2 is reachable THROUGH the ALB, not itself a seed).
func (f *exposureFixture) addEC2Instance(id string) {
	f.t.Helper()
	f.addNode(id, "ec2-instance", map[string]any{}, nil)
}

// addRDSInstance creates an rds-instance node with PubliclyAccessible=false.
// awsRDSInstanceSensitive flags every RDS regardless of publicness, so the
// walker terminates here with the relational-database reason.
func (f *exposureFixture) addRDSInstance(id string) {
	f.t.Helper()
	f.addNode(id, "rds-instance", map[string]any{
		"PubliclyAccessible": false,
	}, nil)
}

// addIAMRoleWithAdminFinding creates an iam-role node AND persists a
// matching topology:iam_escalation finding in the knowledge graph so that
// awsIAMRoleSensitive flags the role as a sensitive terminal.
//
// This is the PERSISTENCE-FAKE path: the public-exposure analyzer consumes
// findings via classifySensitive which calls iamRoleHasEscalationFinding,
// which queries the knowledge graph for the finding. Tests use this helper
// to stand in for the real iam_escalation analyzer without running it.
func (f *exposureFixture) addIAMRoleWithAdminFinding(id string) {
	f.t.Helper()
	f.addNode(id, "iam-role", map[string]any{}, nil)
	f.fx.AddKnowledgeFinding("finding:"+id, "iam_escalation", id)
}

// addPublicS3Bucket creates an s3-bucket node with both
// public_access_block_missing=true AND an ACL public grant. The
// awsS3SeedRule scores this as the maximum S3 severity (0.95) because
// ACL grants are the worst case.
func (f *exposureFixture) addPublicS3Bucket(id string) {
	f.t.Helper()
	f.addNode(id, "s3-bucket", map[string]any{
		"public_access_block_missing": true,
		"acl_public_grants": []map[string]any{
			{"group_uri": "http://acs.amazonaws.com/groups/global/AllUsers", "permission": "READ"},
		},
	}, nil)
}

// addLockedDownS3Bucket creates an s3-bucket with PAB fully enabled and
// no public grants — used by the negative LockedDown test to verify the
// seed rule does NOT fire.
func (f *exposureFixture) addLockedDownS3Bucket(id string) {
	f.t.Helper()
	f.addNode(id, "s3-bucket", map[string]any{
		"public_access_block": map[string]any{
			"block_public_acls":       true,
			"block_public_policy":     true,
			"ignore_public_acls":      true,
			"restrict_public_buckets": true,
		},
		"bucket_policy_status": map[string]any{"is_public": false},
	}, nil)
}

// addK8sLoadBalancerService creates a Kubernetes Service node of
// type=LoadBalancer (flattened into metadata["type"] by the collector so
// the k8sServiceSeedRule fast-path matches without re-parsing content).
func (f *exposureFixture) addK8sLoadBalancerService(id, namespace string) {
	f.t.Helper()
	f.addNode(id, "Service", map[string]any{
		"spec": map[string]any{"type": "LoadBalancer"},
	}, map[string]string{
		"type":      "LoadBalancer",
		"namespace": namespace,
	})
}

// addK8sClusterIPService creates a cluster-internal Service — used by
// negative tests to confirm it does NOT seed the walker.
func (f *exposureFixture) addK8sClusterIPService(id, namespace string) {
	f.t.Helper()
	f.addNode(id, "Service", map[string]any{
		"spec": map[string]any{"type": "ClusterIP"},
	}, map[string]string{
		"type":      "ClusterIP",
		"namespace": namespace,
	})
}

// addK8sPod creates a Pod node in the given namespace.
func (f *exposureFixture) addK8sPod(id, namespace string) {
	f.t.Helper()
	f.addNode(id, "Pod", map[string]any{}, map[string]string{
		"namespace": namespace,
	})
}

// addK8sSecret creates a Secret node. The k8sSecretSensitive rule flags
// every Secret as sensitive with score 0.9.
func (f *exposureFixture) addK8sSecret(id, namespace string) {
	f.t.Helper()
	f.addNode(id, "Secret", map[string]any{}, map[string]string{
		"namespace": namespace,
	})
}

// addK8sServiceAccountWithIRSA creates a ServiceAccount node annotated
// with irsa_role_arn metadata pointing at the given role ARN. Combined
// with addIAMRoleWithAdminFinding on the same role, this makes the SA
// the cross-graph pivot the unified walker uses for its IRSA test.
func (f *exposureFixture) addK8sServiceAccountWithIRSA(id, namespace, roleARN string) {
	f.t.Helper()
	f.addNode(id, "ServiceAccount", map[string]any{}, map[string]string{
		"namespace":     namespace,
		"irsa_role_arn": roleARN,
	})
}

// runAWSAnalyzer invokes AWSPublicExposureAnalyzer.Run against the
// fixture and returns the emitted findings, failing the test on error.
func (f *exposureFixture) runAWSAnalyzer() []Finding {
	f.t.Helper()
	findings, err := AWSPublicExposureAnalyzer{}.Run(newTestCtx(f.t), f.fx.cloudReq(peE2EAccount, 0))
	require.NoError(f.t, err)
	return findings
}

// runK8sAnalyzer invokes K8sPublicExposureAnalyzer.Run against the fixture.
func (f *exposureFixture) runK8sAnalyzer() []Finding {
	f.t.Helper()
	findings, err := K8sPublicExposureAnalyzer{}.Run(newTestCtx(f.t), f.fx.cloudReq(peE2EAccount, 0))
	require.NoError(f.t, err)
	return findings
}

// runUnifiedAnalyzer invokes UnifiedPublicExposureAnalyzer.Run against
// the fixture and returns the emitted findings.
func (f *exposureFixture) runUnifiedAnalyzer() []Finding {
	f.t.Helper()
	findings, err := UnifiedPublicExposureAnalyzer{}.Run(newTestCtx(f.t), f.fx.cloudReq(peE2EAccount, 0))
	require.NoError(f.t, err)
	return findings
}

// TestExposureFixture_Smoke is a sanity check that the helper-built
// graph is shaped correctly: ALB seed enumerates, EC2 and RDS nodes exist
// and do NOT enumerate as seeds, and the IAM-role finding lookup works.
func TestExposureFixture_Smoke(t *testing.T) {
	fx := newExposureFixture(t)
	fx.addInternetFacingALB("arn:alb:smoke")
	fx.addEC2Instance("arn:ec2:smoke")
	fx.addRDSInstance("arn:rds:smoke")
	fx.addIAMRoleWithAdminFinding("arn:aws:iam::000000000001:role/smoke-admin")

	seeds := enumerateSeeds(newTestCtx(t), fx.fx.reader(fx.Account()), "aws")
	// Only the ALB should seed; EC2 has no public IP, RDS has
	// PubliclyAccessible=false, and iam-role is not a seed rule type.
	require.Len(t, seeds, 1)
	require.Equal(t, "arn:alb:smoke", seeds[0].NodeID)

	// The IAM escalation finding must resolve via iamRoleHasEscalationFinding.
	require.True(t,
		iamRoleHasEscalationFinding(newTestCtx(t), fx.caller(), "arn:aws:iam::000000000001:role/smoke-admin"),
		"IAM escalation finding must be visible to the walker")
}
