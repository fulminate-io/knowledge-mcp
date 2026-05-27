// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// k8s_reachability_partial_test.go covers Phase 2.5 Step 4: per-(protocol,
// port) finding emission. The canonical recipe is a policy allowing ingress
// only on TCP/80 for one pod pair; the classifier must emit two partial-
// reachability findings — one for TCP/80 (reachable) and one for TCP/443
// (unreachable) — and each must carry protocol/port metadata.

// buildPartialReachabilityFixture builds the canonical recipe:
//   - web, api pods
//   - deny-all-ingress NetworkPolicy restricts both
//   - allow-web→api on TCP/80 only
//   - allow-web→api on TCP/443 only (a SECOND allow rule on a DIFFERENT pod
//     not api — so that api receives no 443 allow)
//
// The ONLY probes in the index are (TCP, 80) and (TCP, 443). canReach(web,
// api, TCP, 80) is true; canReach(web, api, TCP, 443) is false. The
// classifier must surface both via per-probe partial-reachability findings.
func buildPartialReachabilityFixture(t *testing.T) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)

	web := addPod(fx, "default/Pod/web", "default")
	api := addPod(fx, "default/Pod/api", "default")
	other := addPod(fx, "default/Pod/other", "default")

	denyAll := addNetworkPolicy(fx, "default/NetworkPolicy/deny-all", "default")
	addRestrictsEdge(t, fx, denyAll, web, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, api, kgtypes.EdgeRestrictsIngress)
	addRestrictsEdge(t, fx, denyAll, other, kgtypes.EdgeRestrictsIngress)

	// Allow web → api on TCP/80 only.
	addAllowEdge(t, fx, api, web, edgePortMetadata{
		Protocol: "TCP",
		PortFrom: 80,
		PortTo:   80,
	})
	// Allow web → other on TCP/443 so (TCP, 443) is part of the probe set
	// in collectPortProbes. Without a 443 allow edge ANYWHERE, probes
	// wouldn't include 443 at all and the test couldn't exercise the
	// "unreachable" branch.
	addAllowEdge(t, fx, other, web, edgePortMetadata{
		Protocol: "TCP",
		PortFrom: 443,
		PortTo:   443,
	})
	return fx
}

// TestPartialReachability_RecipeTCP80vs443 verifies the canonical per-port
// recipe: a policy allowing ingress only on TCP/80 produces separate
// findings for TCP/80 (reachable) and TCP/443 (unreachable) for (web, api).
func TestPartialReachability_RecipeTCP80vs443(t *testing.T) {
	fx := buildPartialReachabilityFixture(t)
	findings := runClassify(t, fx, nil)

	var web80, web443 *Finding
	for i := range findings {
		f := &findings[i]
		if indexOf(f.Title, "Partial reachability default/Pod/web → default/Pod/api") < 0 {
			continue
		}
		if f.Metadata["port"] == "80" && f.Metadata["protocol"] == "TCP" {
			web80 = f
		}
		if f.Metadata["port"] == "443" && f.Metadata["protocol"] == "TCP" {
			web443 = f
		}
	}
	require.NotNil(t, web80, "expected a TCP/80 partial reachability finding for web → api")
	require.NotNil(t, web443, "expected a TCP/443 partial reachability finding for web → api")

	assert.Contains(t, web80.Title, "reachable")
	assert.NotContains(t, web80.Title, "unreachable")
	assert.Contains(t, web443.Title, "unreachable")

	// Step 4 criterion: protocol/port metadata keys present when the
	// distinction matters.
	assert.Equal(t, "TCP", web80.Metadata["protocol"])
	assert.Equal(t, "80", web80.Metadata["port"])
	assert.Equal(t, "TCP", web443.Metadata["protocol"])
	assert.Equal(t, "443", web443.Metadata["port"])
}

// TestPartialReachability_UniformCollapse verifies that a pod pair whose
// reachability is uniform across all probes does NOT emit partial
// reachability findings — the collapse rule prevents cardinality explosion.
func TestPartialReachability_UniformCollapse(t *testing.T) {
	fx := newCloudFixture(t)
	a := addPod(fx, "default/Pod/a", "default")
	b := addPod(fx, "default/Pod/b", "default")
	_ = a
	_ = b
	// No NetworkPolicy at all → default-allow both directions on every
	// probe → uniform across the probe set → no partial findings.
	findings := runClassify(t, fx, nil)
	for _, f := range findings {
		assert.NotContains(t, f.Title, "Partial reachability",
			"uniform pairs must not produce partial findings")
	}
}
