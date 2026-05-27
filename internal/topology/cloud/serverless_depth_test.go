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

// serverless_depth_test.go covers ServerlessDepthAnalyzer: deep dependency
// chain, no dependencies, multiple functions sorted by depth, empty graph,
// non-cloud graph, and TopK capping. The graph is served over the wire.

const sdAccount = "serverless-depth-test"

func runServerlessDepth(t *testing.T, fx *cloudFixture, topK int) []foundation.Finding {
	t.Helper()
	findings, err := ServerlessDepthAnalyzer{}.Run(context.Background(), fx.cloudReq(sdAccount, topK))
	require.NoError(t, err)
	return findings
}

func TestServerlessDepthAnalyzer_Name(t *testing.T) {
	assert.Equal(t, "serverless_depth", ServerlessDepthAnalyzer{}.Name())
}

// TestServerlessDepth_DeepChain builds a multi-level dependency tree under a
// Lambda and checks the BFS depth + count metrics.
func TestServerlessDepth_DeepChain(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(sdAccount, "fn-deep", "fn-deep", "lambda-function", nil)
	fx.AddCloudResource(sdAccount, "subnet-1", "subnet-1", "subnet", nil)
	fx.AddCloudResource(sdAccount, "vpc-1", "vpc-1", "vpc", nil)
	fx.AddCloudResource(sdAccount, "nat-gw", "nat-gw", "nat-gateway", nil)
	fx.AddCloudResource(sdAccount, "role-1", "role-1", "iam-role", nil)
	fx.AddCloudResource(sdAccount, "secret-1", "secret-1", "secretsmanager-secret", nil)
	fx.AddCloudResource(sdAccount, "kms-1", "kms-1", "kms-key", nil)

	fx.AddEdge(sdAccount, "fn-deep", "subnet-1", kgtypes.EdgeUsesSubnet)
	fx.AddEdge(sdAccount, "subnet-1", "vpc-1", kgtypes.EdgeUsesNetwork)
	fx.AddEdge(sdAccount, "fn-deep", "role-1", kgtypes.EdgeAssumesRole)
	fx.AddEdge(sdAccount, "fn-deep", "secret-1", kgtypes.EdgeMountsSecret)
	fx.AddEdge(sdAccount, "fn-deep", "kms-1", kgtypes.EdgeEncryptsWith)
	fx.AddEdge(sdAccount, "vpc-1", "nat-gw", kgtypes.EdgeUsesNetwork)

	findings := runServerlessDepth(t, fx, 0)
	require.Len(t, findings, 1)

	f := findings[0]
	assert.Equal(t, "serverless_depth", f.Algorithm)
	assert.GreaterOrEqual(t, f.Metrics["dependency_depth"], float64(2),
		"depth should reflect the BFS tree depth")
	assert.GreaterOrEqual(t, f.Metrics["dependency_count"], float64(4),
		"should count unique dependencies")
	assert.Contains(t, f.Evidence, "fn-deep")
}

// TestServerlessDepth_NoDependencies verifies a Lambda with no outgoing
// dependency edges reports depth 0.
func TestServerlessDepth_NoDependencies(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(sdAccount, "fn-iso", "fn-iso", "lambda-function", nil)

	findings := runServerlessDepth(t, fx, 0)
	require.Len(t, findings, 1)

	f := findings[0]
	assert.InDelta(t, 0, f.Metrics["dependency_depth"], 0.01)
	assert.InDelta(t, 0, f.Metrics["dependency_count"], 0.01)
	assert.Equal(t, foundation.SeverityInfo, f.Severity)
}

// TestServerlessDepth_MultipleFunctions verifies findings are sorted by
// depth descending: deepest function first.
func TestServerlessDepth_MultipleFunctions(t *testing.T) {
	fx := newCloudFixture(t)

	// Function A: depth 0 (no deps).
	fx.AddCloudResource(sdAccount, "fn-a", "fn-a", "lambda-function", nil)

	// Function B: depth 2 (fn -> subnet -> vpc).
	fx.AddCloudResource(sdAccount, "fn-b", "fn-b", "lambda-function", nil)
	fx.AddCloudResource(sdAccount, "sub-b", "sub-b", "subnet", nil)
	fx.AddCloudResource(sdAccount, "vpc-b", "vpc-b", "vpc", nil)
	fx.AddEdge(sdAccount, "fn-b", "sub-b", kgtypes.EdgeUsesSubnet)
	fx.AddEdge(sdAccount, "sub-b", "vpc-b", kgtypes.EdgeUsesNetwork)

	// Function C: depth 1 (fn -> role).
	fx.AddCloudResource(sdAccount, "fn-c", "fn-c", "lambda-function", nil)
	fx.AddCloudResource(sdAccount, "role-c", "role-c", "iam-role", nil)
	fx.AddEdge(sdAccount, "fn-c", "role-c", kgtypes.EdgeAssumesRole)

	findings := runServerlessDepth(t, fx, 0)
	require.Len(t, findings, 3)

	// Sorted by depth descending: fn-b (2), fn-c (1), fn-a (0).
	assert.InDelta(t, 2, findings[0].Metrics["dependency_depth"], 0.01)
	assert.InDelta(t, 1, findings[1].Metrics["dependency_depth"], 0.01)
	assert.InDelta(t, 0, findings[2].Metrics["dependency_depth"], 0.01)
}

// TestServerlessDepth_GCPFunction verifies GCP Cloud Functions are also
// detected as serverless functions.
func TestServerlessDepth_GCPFunction(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(sdAccount, "gcf-1", "gcf-1", "gcp:cloudfunctions:function", nil)
	fx.AddCloudResource(sdAccount, "gcp-sa", "gcp-sa", "gcp:iam:serviceAccount", nil)
	fx.AddEdge(sdAccount, "gcf-1", "gcp-sa", kgtypes.EdgeUsesSA)

	findings := runServerlessDepth(t, fx, 0)
	require.Len(t, findings, 1)
	assert.InDelta(t, 1, findings[0].Metrics["dependency_depth"], 0.01)
}

// TestServerlessDepth_EmptyGraph verifies no serverless functions means nil.
func TestServerlessDepth_EmptyGraph(t *testing.T) {
	fx := newCloudFixture(t)
	fx.account(sdAccount)
	findings := runServerlessDepth(t, fx, 0)
	assert.Nil(t, findings)
}

// TestServerlessDepth_NonCloudGraph verifies error on wrong graph type.
func TestServerlessDepth_NonCloudGraph(t *testing.T) {
	fx := newCloudFixture(t)
	req := foundation.Request{Caller: fx, Graph: kgtypes.GraphKnowledge, Name: "default"}
	_, err := ServerlessDepthAnalyzer{}.Run(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires GraphCloud")
}

// TestServerlessDepth_TopK verifies the TopK cap.
func TestServerlessDepth_TopK(t *testing.T) {
	fx := newCloudFixture(t)
	for i := range 5 {
		id := fmt.Sprintf("fn-topk-%d", i)
		fx.AddCloudResource(sdAccount, id, id, "lambda-function", nil)
	}

	findings := runServerlessDepth(t, fx, 2)
	assert.Len(t, findings, 2)
}
