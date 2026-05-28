// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// orphan_analyzer_test.go covers OrphanAnalyzer.Run end-to-end: it builds
// a small mixed-provider cloud graph (served over the wire), runs the
// analyzer, and asserts the expected orphans surface in the output. The
// dispatch loop, scoping, resource_type filtering, TopK truncation, Subset
// filtering, error handling for non-cloud graphs, and finding ordering all
// live in OrphanAnalyzer.Run.

const e2eAcct = "987654321098"

// TestOrphanAnalyzer_Name pins the analyzer name.
func TestOrphanAnalyzer_Name(t *testing.T) {
	assert.Equal(t, "orphan", OrphanAnalyzer{}.Name())
}

// TestOrphanAnalyzer_Registered confirms the analyzer self-registered with
// the topology registry at init() time.
func TestOrphanAnalyzer_Registered(t *testing.T) {
	a, ok := foundation.Get("orphan")
	require.True(t, ok)
	require.NotNil(t, a)
	assert.Equal(t, "orphan", a.Name())
}

// TestOrphanAnalyzer_Run_NonCloudGraph_ReturnsError verifies that calling
// Run with anything other than GraphCloud/GraphCICD is rejected.
func TestOrphanAnalyzer_Run_NonCloudGraph_ReturnsError(t *testing.T) {
	fx := newCloudFixture(t)
	req := foundation.Request{Caller: fx, Graph: kgtypes.GraphKnowledge, Name: "default"}
	_, err := OrphanAnalyzer{}.Run(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GraphCloud")
}

// TestOrphanAnalyzer_Run_EmptyGraph verifies that an empty cloud graph
// returns (nil, nil) — no findings, no error.
func TestOrphanAnalyzer_Run_EmptyGraph(t *testing.T) {
	fx := newCloudFixture(t)
	fx.account(e2eAcct)
	findings, err := OrphanAnalyzer{}.Run(context.Background(), fx.cloudReq(e2eAcct, 0))
	require.NoError(t, err)
	assert.Nil(t, findings)
}

// TestOrphanAnalyzer_Run_NilCaller returns an error rather than panicking.
func TestOrphanAnalyzer_Run_NilCaller(t *testing.T) {
	req := foundation.Request{Graph: kgtypes.GraphCloud, Name: e2eAcct}
	_, err := OrphanAnalyzer{}.Run(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Caller")
}

// TestOrphanAnalyzer_Run_EndToEnd builds a mixed-provider cloud graph mixing
// orphans and non-orphans across all four providers and asserts the analyzer
// surfaces exactly the orphans we planted.
func TestOrphanAnalyzer_Run_EndToEnd(t *testing.T) {
	fx := newCloudFixture(t)

	// AWS: orphan EBS, attached EBS, orphan SG, used SG, orphan IAM role.
	fx.AddCloudResource(e2eAcct, "vol-orphan", "vol-orphan", "ebs-volume", nil)
	fx.AddCloudResource(e2eAcct, "vol-attached", "vol-attached", "ebs-volume", nil)
	fx.AddCloudResource(e2eAcct, "i-1", "i-1", "ec2-instance", nil)
	fx.AddEdge(e2eAcct, "vol-attached", "i-1", kgtypes.EdgeBoundTo)

	fx.AddCloudResource(e2eAcct, "sg-orphan", "sg-orphan", "security-group", nil)
	fx.AddCloudResource(e2eAcct, "sg-used", "sg-used", "security-group", nil)
	fx.AddEdge(e2eAcct, "i-1", "sg-used", kgtypes.EdgeUsesSecurityGroup)

	fx.AddCloudResource(e2eAcct, "arn:aws:iam::987:role/lonely", "lonely", "iam-role", nil)

	// K8s: orphan service, service that selects pods, static pod, bare pod.
	fx.AddCloudResource(e2eAcct, "default/Service/orphan", "orphan", "Service", nil)

	fx.AddCloudResource(e2eAcct, "default/Service/web", "web", "Service", nil)
	fx.AddCloudResource(e2eAcct, "default/Pod/web-abc", "web-abc", "Pod", nil)
	fx.AddCloudResource(e2eAcct, "default/ReplicaSet/web", "web", "ReplicaSet", nil)
	fx.AddEdge(e2eAcct, "default/Service/web", "default/Pod/web-abc", kgtypes.EdgeSelects)
	fx.AddEdge(e2eAcct, "default/Pod/web-abc", "default/ReplicaSet/web", kgtypes.EdgeOwnedBy)

	fx.AddCloudResource(e2eAcct, "kube-system/Pod/etcd-node1", "etcd-node1", "Pod",
		map[string]string{"annotation/kubernetes.io/config.source": "file"})

	// GCP: orphan forwardingRule.
	fx.AddCloudResource(e2eAcct, "fw-orphan", "fw-orphan", "gcp:compute:forwardingRule", nil)

	// Azure: orphan loadBalancer.
	fx.AddCloudResource(e2eAcct, "azure-lb", "azure-lb", "Microsoft.Network/loadBalancers", nil)

	findings, err := OrphanAnalyzer{}.Run(context.Background(), fx.cloudReq(e2eAcct, 0))
	require.NoError(t, err)

	// Build a set of evidence IDs for assertions.
	gotByEvidence := make(map[string]foundation.Finding, len(findings))
	for _, f := range findings {
		require.NotEmpty(t, f.Evidence, "every finding must carry primary evidence")
		gotByEvidence[f.Evidence[0]] = f
	}

	expectedOrphans := []string{
		"vol-orphan",
		"sg-orphan",
		"arn:aws:iam::987:role/lonely",
		"default/Service/orphan",
		"fw-orphan",
		"azure-lb",
	}
	for _, id := range expectedOrphans {
		_, ok := gotByEvidence[id]
		assert.Truef(t, ok, "expected orphan finding for %s, got: %v", id, mapKeys(gotByEvidence))
	}

	notExpected := []string{
		"vol-attached",
		"sg-used",
		"i-1",
		"default/Service/web",
		"default/Pod/web-abc",
		"default/ReplicaSet/web",
		"kube-system/Pod/etcd-node1",
	}
	for _, id := range notExpected {
		_, ok := gotByEvidence[id]
		assert.Falsef(t, ok, "%s must NOT appear as orphan", id)
	}

	// Confidence ordering: highest first, deterministic tie-breaking by ID.
	for i := 1; i < len(findings); i++ {
		ci := findings[i-1].Metrics["confidence"]
		cj := findings[i].Metrics["confidence"]
		if ci == cj {
			assert.LessOrEqualf(t, findings[i-1].Evidence[0], findings[i].Evidence[0],
				"ties on confidence must break by evidence ID ascending")
		} else {
			assert.Greaterf(t, ci, cj, "findings must be sorted by confidence descending")
		}
	}

	for _, f := range findings {
		assert.Equal(t, "orphan", f.Algorithm)
	}
}

// TestOrphanAnalyzer_Run_TopK truncates the result set.
func TestOrphanAnalyzer_Run_TopK(t *testing.T) {
	fx := newCloudFixture(t)
	for i := range 5 {
		id := idForIndex("vol", i)
		fx.AddCloudResource(e2eAcct, id, id, "ebs-volume", nil)
	}

	findings, err := OrphanAnalyzer{}.Run(context.Background(), fx.cloudReq(e2eAcct, 3))
	require.NoError(t, err)
	assert.Len(t, findings, 3)
}

// TestOrphanAnalyzer_Run_Subset filters which nodes participate.
func TestOrphanAnalyzer_Run_Subset(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(e2eAcct, "vol-keep", "vol-keep", "ebs-volume", nil)
	fx.AddCloudResource(e2eAcct, "vol-skip", "vol-skip", "ebs-volume", nil)

	req := fx.cloudReq(e2eAcct, 0)
	req.Subset = func(n *knowledgev1.Node) bool { return n.GetSymbolName() == "vol-keep" }

	findings, err := OrphanAnalyzer{}.Run(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, "vol-keep", findings[0].Evidence[0])
}

// idForIndex builds a deterministic synthetic ID for the TopK test.
func idForIndex(prefix string, i int) string {
	return prefix + "-" + string(rune('0'+i))
}

// mapKeys returns the keys of a map[string]Finding for diagnostic output.
func mapKeys(m map[string]foundation.Finding) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
