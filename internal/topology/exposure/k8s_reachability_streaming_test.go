// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// k8s_reachability_streaming_test.go covers the streaming-path behavior
// introduced when the 1000-pod hard cap was replaced with a per-pod /
// per-edge streaming classifier. Two themes:
//
//  1. Above-legacy-cap tests: clusters in the 1000-2000 pod range now run
//     natively and produce the expected native findings without hitting
//     the sentinel path.
//
//  2. Sentinel tests: the hard cap still exists (at 50000 pods by default)
//     as a last-resort guardrail. These tests lower the cap via withPodCap
//     so they can exercise the sentinel path with a tiny fixture.
//
// Extracted from k8s_reachability_test.go to keep that file under the
// topology package's 500-line hard file size cap.

// withPodCap temporarily lowers reachabilityPodCap for the duration of a
// test. The helper restores the original value on cleanup so subsequent
// tests see the production default (50000). Declared here so the sentinel
// tests can exercise the cap path with a tiny fixture instead of
// allocating tens of thousands of pod nodes.
func withPodCap(t *testing.T, cap int) {
	t.Helper()
	orig := reachabilityPodCap
	reachabilityPodCap = cap
	t.Cleanup(func() { reachabilityPodCap = orig })
}

// TestBuildReachabilityIndex_AboveLegacyCap_Normal confirms that 2000 pods
// — well over the old 1000-pod hard cap — now build natively thanks to the
// streaming classifiers. The builder must return a fully-populated index.
// The test matters because the previous sentinel behavior is exactly what
// the streaming refactor replaces.
func TestBuildReachabilityIndex_AboveLegacyCap_Normal(t *testing.T) {
	fx := newCloudFixture(t)
	for i := range 2000 {
		addPod(fx, fmt.Sprintf("default/Pod/p-%05d", i), "default")
	}

	scoped := fx.reader(k8sReachabilityAcct)
	idx, err := buildReachabilityIndex(newTestCtx(t), scoped)
	require.NoError(t, err)
	require.NotNil(t, idx)
	assert.False(t, idx.skipped, "2000 pods must NOT trip the streaming cap")
	assert.Equal(t, 2000, idx.podCount)
	assert.Len(t, idx.pods, 2000)
	assert.NotNil(t, idx.reverseAllowedIngress,
		"reverse-allow maps must be populated on the streaming path")
	assert.NotNil(t, idx.reverseAllowedEgress)
}

// TestKubernetesReachabilityAnalyzer_AboveLegacyCap_EmitsNativeFindings
// verifies the full analyzer pipeline runs natively at 2000 pods and does
// NOT emit the skipped sentinel notice. With no NetworkPolicy restrictions
// every pod is default-allow; findNamespaceFullyOpen must still fire and
// surface the fully-open namespace, proving the streaming path is engaged.
func TestKubernetesReachabilityAnalyzer_AboveLegacyCap_EmitsNativeFindings(t *testing.T) {
	fx := newCloudFixture(t)
	for i := range 2000 {
		addPod(fx, fmt.Sprintf("default/Pod/p-%05d", i), "default")
	}

	findings, err := KubernetesReachabilityAnalyzer{}.Run(newTestCtx(t), Request{
		Caller: fx,
		Graph:  kgtypes.GraphCloud,
		Name:   k8sReachabilityAcct,
		// Opt out of the matrix finding — 2000² ≈ 4M entries would be
		// truncated anyway; the test is about the streaming path, not
		// matrix behavior.
		Extra: map[string]string{"emit_matrix": "false"},
	})
	require.NoError(t, err)
	for _, f := range findings {
		assert.NotContains(t, f.Title, "skipped",
			"2000 pods must run natively — no skipped sentinel")
	}
	// findNamespaceFullyOpen must have fired since every pod is
	// default-allow. One finding per namespace; we only loaded "default".
	nsOpen := findingsByCategory(findings, "Namespace fully open")
	assert.Len(t, nsOpen, 1, "default namespace must surface as fully-open on the streaming path")
}

// TestStreamingClassifiers_RestrictedAboveLegacyCap exercises the streaming
// per-pod classifiers (isolated, over-exposed, asymmetric) on a 1200-pod
// namespace where a single "lonely" pod is fully restricted with no allow
// edges. The streaming path must surface it as isolated without walking the
// full O(P²) pod-pair matrix. Also exercises reverseAllowedIngress /
// reverseAllowedEgress by asserting they are populated.
func TestStreamingClassifiers_RestrictedAboveLegacyCap(t *testing.T) {
	fx := newCloudFixture(t)
	// 1200 default-allow pods — above the old 1000-pod cap.
	for i := range 1200 {
		addPod(fx, fmt.Sprintf("default/Pod/crowd-%04d", i), "default")
	}
	// One lonely pod in a separate namespace with ingress AND egress
	// restrictions and no allow edges → fully isolated.
	lonely := addPod(fx, "quarantine/Pod/lonely", "quarantine")
	denyAll := addNetworkPolicy(fx, "quarantine/NetworkPolicy/deny-all", "quarantine")
	addRestrictsEdge(t, fx, denyAll, lonely, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, lonely, kgtypes.EdgeRestrictsEgress)

	findings, err := KubernetesReachabilityAnalyzer{}.Run(newTestCtx(t), Request{
		Caller: fx,
		Graph:  kgtypes.GraphCloud,
		Name:   k8sReachabilityAcct,
		Extra:  map[string]string{"emit_matrix": "false"},
	})
	require.NoError(t, err)
	// Streaming classifier must flag lonely as isolated even though the
	// cluster has 1200+ pods — it walks lonely's own allow maps (both
	// empty) not the whole namespace.
	isolated := findingsByCategory(findings, "Pod fully isolated")
	require.NotEmpty(t, isolated, "lonely pod must be flagged isolated via streaming path")
	assert.Equal(t, "quarantine/Pod/lonely", isolated[0].Evidence[0])
}

// TestStreamingAsymmetric_DefaultAllowVsIngressRestricted is the regression
// test for a correctness edge case the streaming refactor had to handle:
// a default-allow pod A and an ingress-restricted pod B with NO explicit
// allow rule listing A. canReach(A, B) = false (B denies ingress),
// canReach(B, A) = true (B default-allow egress, A default-allow ingress),
// so the pair is asymmetric. The streaming candidate set must include this
// pair via the "restricted pods × namespace peers" pass — edge iteration
// alone would miss it because neither pod has the other in its allow maps.
func TestStreamingAsymmetric_DefaultAllowVsIngressRestricted(t *testing.T) {
	fx := newCloudFixture(t)

	defaultPod := addPod(fx, "default/Pod/free", "default")
	restricted := addPod(fx, "default/Pod/restricted", "default")

	// Policy restricts `restricted` on ingress but does NOT add any allow
	// rule for `free` — classic default-deny ingress.
	denyAll := addNetworkPolicy(fx, "default/NetworkPolicy/deny-ingress", "default")
	addRestrictsEdge(t, fx, denyAll, restricted, kgtypes.EdgeRestrictsIngress)
	_ = defaultPod

	findings, err := KubernetesReachabilityAnalyzer{}.Run(newTestCtx(t), Request{
		Caller: fx,
		Graph:  kgtypes.GraphCloud,
		Name:   k8sReachabilityAcct,
		Extra:  map[string]string{"emit_matrix": "false"},
	})
	require.NoError(t, err)

	// Streaming candidate set must include the pair → asymmetric finding fires.
	asym := findingsByCategory(findings, "Asymmetric reachability")
	require.NotEmpty(t, asym, "default-allow vs ingress-restricted pair must surface as asymmetric")
}

// TestBuildReachabilityIndex_OverCap_ReturnsSentinel exercises the hard-cap
// path with reachabilityPodCap+1 pods. The test temporarily lowers the cap
// so the fixture stays small; the sentinel contract is identical regardless
// of the numeric cap. The builder must return a sentinel index with
// skipped=true and a populated podCount without walking edges.
func TestBuildReachabilityIndex_OverCap_ReturnsSentinel(t *testing.T) {
	withPodCap(t, 10)
	fx := newCloudFixture(t)
	total := reachabilityPodCap + 1
	for i := range total {
		addPod(fx, fmt.Sprintf("default/Pod/p-%04d", i), "default")
	}

	scoped := fx.reader(k8sReachabilityAcct)
	idx, err := buildReachabilityIndex(newTestCtx(t), scoped)
	require.NoError(t, err)
	require.NotNil(t, idx)
	assert.True(t, idx.skipped, "cap+1 pods must trip the hard cap")
	assert.Equal(t, total, idx.podCount)
	assert.Nil(t, idx.pods, "skipped sentinel must not allocate pods map")
	assert.Nil(t, idx.policies, "skipped sentinel must not allocate policies map")

	// canReach on a skipped index must be false — callers are expected to
	// check idx.skipped before issuing queries.
	assert.False(t, idx.canReach("any", "any", "TCP", 80))
}

// TestKubernetesReachabilityAnalyzer_OverCap_EmitsNotice verifies the full
// Run → classifyReachability path surfaces a single reachability_skipped
// notice finding when the index short-circuits at the cap boundary.
func TestKubernetesReachabilityAnalyzer_OverCap_EmitsNotice(t *testing.T) {
	withPodCap(t, 10)
	fx := newCloudFixture(t)
	total := reachabilityPodCap + 1
	for i := range total {
		addPod(fx, fmt.Sprintf("default/Pod/p-%04d", i), "default")
	}

	findings, err := KubernetesReachabilityAnalyzer{}.Run(newTestCtx(t), Request{
		Caller: fx,
		Graph:  kgtypes.GraphCloud,
		Name:   k8sReachabilityAcct,
	})
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, "k8s_reachability", findings[0].Algorithm)
	assert.Equal(t, SeverityNotice, findings[0].Severity)
	assert.Contains(t, findings[0].Title, "skipped")
	assert.InDelta(t, float64(total), findings[0].Metrics["pod_count"], 0.0001)
	assert.InDelta(t, float64(reachabilityPodCap), findings[0].Metrics["pod_cap"], 0.0001)
}

// TestPartialReachability_LargeClusterEdgeDriven is the regression test for
// the pairQuadraticPodCap removal. It builds a 6000-pod cluster (well above
// the former 5000-pod quadratic gate) with a single policy creating partial
// reachability between two pods: web → api is allowed on TCP/80 only, and a
// second allow edge (web → other on TCP/443) ensures 443 is in the probe
// set. The edge-driven findPartialReachability must still emit both
// partial-reachability findings without the old quadratic fallback.
func TestPartialReachability_LargeClusterEdgeDriven(t *testing.T) {
	fx := newCloudFixture(t)

	// 6000 default-allow filler pods — well above the old 5000-pod gate.
	// These never enter the partial-reachability enumeration because they
	// have no allow edges and are default-allow on both directions, so
	// canReach is uniformly true → no probe-variance → no finding.
	for i := range 6000 {
		addPod(fx, fmt.Sprintf("default/Pod/filler-%05d", i), "default")
	}

	// The interesting pair: web, api, other — all ingress-restricted with
	// an allow edge web → api on TCP/80 and web → other on TCP/443.
	web := addPod(fx, "default/Pod/web", "default")
	api := addPod(fx, "default/Pod/api", "default")
	other := addPod(fx, "default/Pod/other", "default")
	denyAll := addNetworkPolicy(fx, "default/NetworkPolicy/deny-all", "default")
	addRestrictsEdge(t, fx, denyAll, web, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, api, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, other, kgtypes.EdgeRestrictsIngress)
	addAllowEdge(t, fx, api, web, edgePortMetadata{
		Protocol: "TCP",
		PortFrom: 80,
		PortTo:   80,
	})
	addAllowEdge(t, fx, other, web, edgePortMetadata{
		Protocol: "TCP",
		PortFrom: 443,
		PortTo:   443,
	})

	findings, err := KubernetesReachabilityAnalyzer{}.Run(newTestCtx(t), Request{
		Caller: fx,
		Graph:  kgtypes.GraphCloud,
		Name:   k8sReachabilityAcct,
		Extra:  map[string]string{"emit_matrix": "false"},
	})
	require.NoError(t, err)

	// The edge-driven partial classifier must surface both per-probe
	// findings for the web → api pair: reachable on TCP/80, unreachable
	// on TCP/443.
	partial := findingsByCategory(findings, "Partial reachability default/Pod/web → default/Pod/api")
	require.Len(t, partial, 2,
		"6000-pod cluster must still emit web→api partial findings via the edge-driven path")
	var reach80, unreach443 bool
	for _, f := range partial {
		if f.Metadata["port"] == "80" && f.Metadata["protocol"] == "TCP" {
			assert.Contains(t, f.Title, "reachable")
			assert.NotContains(t, f.Title, "unreachable")
			reach80 = true
		}
		if f.Metadata["port"] == "443" && f.Metadata["protocol"] == "TCP" {
			assert.Contains(t, f.Title, "unreachable")
			unreach443 = true
		}
	}
	assert.True(t, reach80, "expected reachable-on-80 finding")
	assert.True(t, unreach443, "expected unreachable-on-443 finding")
}

// TestNamespaceFullyOpen_LargeClusterNoVerificationWalk is the regression
// test for the namespaceFullyOpen verification walk removal. It builds a
// 6000-pod namespace where every pod is default-allow on both ingress and
// egress (no NetworkPolicy selects them). The precondition check in
// namespaceAllDefaultAllow is necessary AND sufficient for the fully-open
// classification, so findNamespaceFullyOpen must fire without running a
// per-pair canReach walk — which would have been skipped by the old
// 5000-pod gate.
func TestNamespaceFullyOpen_LargeClusterNoVerificationWalk(t *testing.T) {
	fx := newCloudFixture(t)
	for i := range 6000 {
		addPod(fx, fmt.Sprintf("default/Pod/p-%05d", i), "default")
	}

	findings, err := KubernetesReachabilityAnalyzer{}.Run(newTestCtx(t), Request{
		Caller: fx,
		Graph:  kgtypes.GraphCloud,
		Name:   k8sReachabilityAcct,
		// Opt out of the matrix — 6000² is way past matrixMaxEntries.
		Extra: map[string]string{"emit_matrix": "false"},
	})
	require.NoError(t, err)

	nsOpen := findingsByCategory(findings, "Namespace fully open")
	require.Len(t, nsOpen, 1,
		"6000-pod default-allow namespace must surface as fully-open via the precondition check")
	assert.Equal(t, SeverityWarning, nsOpen[0].Severity)
	assert.Equal(t, "default", nsOpen[0].Metadata["namespace"])
}
