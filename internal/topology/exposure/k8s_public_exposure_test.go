// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// k8s_public_exposure_test.go carries the Phase 7 end-to-end tests for
// the Kubernetes-family public-exposure analyzer. The fixture helpers
// live in public_exposure_fixture_test.go.

// TestK8sPublicExposureAnalyzer_E2E_K8sLbSecret is the canonical K8s
// public-exposure scenario: a Service of type=LoadBalancer fronting a
// pod that mounts a Kubernetes Secret. The analyzer must surface at
// least one finding whose terminal is the Secret with severity ≥ warning.
func TestK8sPublicExposureAnalyzer_E2E_K8sLbSecret(t *testing.T) {
	fx := newExposureFixture(t)

	const (
		ns       = "prod"
		svcID    = "prod/Service/web"
		podID    = "prod/Pod/web-abc"
		secretID = "prod/Secret/api-keys"
	)

	fx.addK8sLoadBalancerService(svcID, ns)
	fx.addK8sPod(podID, ns)
	fx.addK8sSecret(secretID, ns)

	// Service -Selects-> Pod -MountsSecret-> Secret.
	fx.link(svcID, podID, kgtypes.EdgeSelects)
	fx.link(podID, secretID, kgtypes.EdgeMountsSecret)

	findings := fx.runK8sAnalyzer()
	require.NotEmpty(t, findings, "k8s analyzer must emit at least one finding")

	var matched int
	for _, f := range findings {
		require.Equal(t, "k8s_public_exposure", f.Algorithm)
		require.NotEmpty(t, f.Evidence)
		terminal := f.Evidence[len(f.Evidence)-1]
		if terminal != secretID {
			continue
		}
		matched++
		require.Equal(t, svcID, f.Evidence[0], "seed must be the LB Service")
		// severity ≥ warning: the mapping for secret (sensitivity 0.9 and
		// composite ≥ 0.3 at 2 hops is 0.9/3 = 0.3) lands on warning.
		require.NotEqual(t, SeverityNotice, f.Severity,
			"Secret terminal must produce at least warning severity, got %v", f.Severity)
		require.Contains(t, f.Summary, "Kubernetes secret")
	}
	require.GreaterOrEqual(t, matched, 1,
		"analyzer must emit at least one Service → Pod → Secret finding")
}
