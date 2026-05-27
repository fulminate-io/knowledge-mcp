// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// cross_provider_blast_test.go covers CrossProviderBlastAnalyzer:
// IRSA bridge discovery, GCP Workload Identity bridge, no bridge,
// and blast score computation.

const cpbAccount = "cross-provider-blast-test"

func runCrossProviderBlast(t *testing.T, fx *cloudFixture, topK int) []foundation.Finding {
	t.Helper()
	findings, err := CrossProviderBlastAnalyzer{}.Run(context.Background(), fx.cloudReq(cpbAccount, topK))
	require.NoError(t, err)
	return findings
}

func TestCrossProviderBlastAnalyzer_Name(t *testing.T) {
	assert.Equal(t, "cross_provider_blast", CrossProviderBlastAnalyzer{}.Name())
}

// TestCrossProviderBlast_IRSA builds:
//
//	K8s SA (irsa_role_arn=arn:iam::123:role/r) -> IAM role -> S3 bucket
//
// Expect a finding with blast_score reflecting reachable resources.
func TestCrossProviderBlast_IRSA(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(cpbAccount, "k8s:sa:app", "app-sa", "ServiceAccount",
		map[string]string{"irsa_role_arn": "arn:aws:iam::123456:role/app-role"})
	fx.AddCloudResource(cpbAccount, "arn:aws:iam::123456:role/app-role", "app-role", "iam-role", nil)
	fx.AddCloudResource(cpbAccount, "arn:s3:bucket-1", "bucket-1", "s3-bucket", nil)
	fx.AddCloudResource(cpbAccount, "arn:s3:bucket-2", "bucket-2", "s3-bucket", nil)

	fx.AddEdge(cpbAccount, "arn:aws:iam::123456:role/app-role", "arn:s3:bucket-1", kgtypes.EdgeTargets)
	fx.AddEdge(cpbAccount, "arn:aws:iam::123456:role/app-role", "arn:s3:bucket-2", kgtypes.EdgeTargets)

	findings := runCrossProviderBlast(t, fx, 0)
	require.Len(t, findings, 1)

	f := findings[0]
	assert.Equal(t, "cross_provider_blast", f.Algorithm)
	assert.GreaterOrEqual(t, f.Metrics["blast_score"], float64(2),
		"blast score should count at least the 2 S3 buckets + IAM role")
	assert.Contains(t, f.Evidence, "k8s:sa:app")
	assert.Contains(t, f.Evidence, "arn:aws:iam::123456:role/app-role")
}

// TestCrossProviderBlast_GCPWorkloadIdentity builds:
//
//	K8s SA (gcp_service_account=sa@proj.iam) -> GCP SA -> GCS bucket
//
// Expect a finding with blast_score.
func TestCrossProviderBlast_GCPWorkloadIdentity(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(cpbAccount, "k8s:sa:gcp-app", "gcp-app-sa", "ServiceAccount",
		map[string]string{"gcp_service_account": "sa@proj.iam.gserviceaccount.com"})
	fx.AddCloudResource(cpbAccount, "sa@proj.iam.gserviceaccount.com", "gcp-sa", "gcp:iam:serviceAccount", nil)
	fx.AddCloudResource(cpbAccount, "gcp:gcs:bucket", "gcs-bucket", "gcp:storage:bucket", nil)

	fx.AddEdge(cpbAccount, "sa@proj.iam.gserviceaccount.com", "gcp:gcs:bucket", kgtypes.EdgeTargets)

	findings := runCrossProviderBlast(t, fx, 0)
	require.Len(t, findings, 1)

	f := findings[0]
	assert.GreaterOrEqual(t, f.Metrics["blast_score"], float64(1))
	assert.Contains(t, f.Evidence, "k8s:sa:gcp-app")
}

// TestCrossProviderBlast_NoBridge verifies a ServiceAccount without
// irsa_role_arn or gcp_service_account produces no finding.
func TestCrossProviderBlast_NoBridge(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(cpbAccount, "k8s:sa:plain", "plain-sa", "ServiceAccount", nil)

	findings := runCrossProviderBlast(t, fx, 0)
	assert.Nil(t, findings, "SA with no bridge should produce no findings")
}

// TestCrossProviderBlast_NoServiceAccounts verifies an empty graph
// (no ServiceAccounts) returns nil.
func TestCrossProviderBlast_NoServiceAccounts(t *testing.T) {
	fx := newCloudFixture(t)
	// Add non-SA resources.
	fx.AddCloudResource(cpbAccount, "ec2-1", "ec2-1", "ec2-instance", nil)

	findings := runCrossProviderBlast(t, fx, 0)
	assert.Nil(t, findings)
}

// TestCrossProviderBlast_DeepChain verifies BFS traverses multiple hops.
func TestCrossProviderBlast_DeepChain(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(cpbAccount, "k8s:sa:deep", "deep-sa", "ServiceAccount",
		map[string]string{"irsa_role_arn": "arn:iam::deep-role"})
	fx.AddCloudResource(cpbAccount, "arn:iam::deep-role", "deep-role", "iam-role", nil)
	fx.AddCloudResource(cpbAccount, "hop-1", "hop-1", "s3-bucket", nil)
	fx.AddCloudResource(cpbAccount, "hop-2", "hop-2", "kms-key", nil)
	fx.AddCloudResource(cpbAccount, "hop-3", "hop-3", "secretsmanager-secret", nil)

	fx.AddEdge(cpbAccount, "arn:iam::deep-role", "hop-1", kgtypes.EdgeTargets)
	fx.AddEdge(cpbAccount, "hop-1", "hop-2", kgtypes.EdgeEncryptsWith)
	fx.AddEdge(cpbAccount, "hop-2", "hop-3", kgtypes.EdgeMountsSecret)

	findings := runCrossProviderBlast(t, fx, 0)
	require.Len(t, findings, 1)

	f := findings[0]
	// Should reach: role + hop-1 + hop-2 + hop-3 = 4 reachable nodes.
	assert.GreaterOrEqual(t, f.Metrics["blast_score"], float64(3),
		"BFS should traverse multiple hops")
}

// TestCrossProviderBlast_TopK verifies the TopK cap.
func TestCrossProviderBlast_TopK(t *testing.T) {
	fx := newCloudFixture(t)
	for i := range 3 {
		saID := fmt.Sprintf("k8s:sa:topk-%d", i)
		roleARN := fmt.Sprintf("arn:iam::topk-role-%d", i)
		bucket := fmt.Sprintf("bucket-topk-%d", i)
		fx.AddCloudResource(cpbAccount, saID, saID, "ServiceAccount",
			map[string]string{"irsa_role_arn": roleARN})
		fx.AddCloudResource(cpbAccount, roleARN, roleARN, "iam-role", nil)
		fx.AddCloudResource(cpbAccount, bucket, bucket, "s3-bucket", nil)
		fx.AddEdge(cpbAccount, roleARN, bucket, kgtypes.EdgeTargets)
	}

	findings := runCrossProviderBlast(t, fx, 1)
	assert.Len(t, findings, 1)
}

// TestCrossProviderBlast_NonCloudGraph verifies non-cloud graph returns
// nil findings (not an error — this analyzer returns nil silently).
func TestCrossProviderBlast_NonCloudGraph(t *testing.T) {
	fx := newCloudFixture(t)
	req := foundation.Request{Caller: fx, Graph: kgtypes.GraphKnowledge, Name: "default"}
	findings, err := CrossProviderBlastAnalyzer{}.Run(context.Background(), req)
	require.NoError(t, err)
	assert.Nil(t, findings)
}
