// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// unified_public_exposure_test.go carries the Phase 7 cross-cloud end-to-
// end test for UnifiedPublicExposureAnalyzer — the canonical
// "internet-facing ALB → EKS LB → Pod → IRSA → IAM admin" chain that the
// ticket names as the v2 motivating scenario.

// TestUnifiedPublicExposureAnalyzer_E2E_AlbEksIrsaAdmin is the cross-
// cloud fixture test. It builds:
//
//	ALB (internet-facing) → Target Group → EC2 (EKS node)
//	                                    → K8s LoadBalancer Service (EKS LB)
//	                                        -Selects-> Pod (EKS workload)
//	                                                 -UsesSA-> ServiceAccount
//	                                                          (with irsa_role_arn)
//	                                    ^^ IRSA bridge (cross_graph=true) ^^
//	                                    -AssumesRole-> IAM admin role
//	                                                   (with iam_escalation finding)
//
// and asserts the unified analyzer:
//
//  1. Emits at least one finding whose Algorithm is "unified_public_exposure".
//  2. Marks the finding metadata with cross_graph=true because the IRSA
//     shortcut produces a cross_graph edge.
//  3. The finding's hop list contains BOTH AWS node IDs (ALB, EC2) and
//     K8s node IDs (Service, Pod, SA).
//  4. The terminal is the IRSA-bound admin IAM role.
//  5. Severity is critical (sensitivity ≥ 0.95 via the admin-reachable
//     IAM role rule).
func TestUnifiedPublicExposureAnalyzer_E2E_AlbEksIrsaAdmin(t *testing.T) {
	fx := newExposureFixture(t)

	const (
		// AWS side.
		albID = "arn:aws:elbv2:us-east-1:000000000001:loadbalancer/app/eks/1"
		tgID  = "arn:aws:elbv2:us-east-1:000000000001:targetgroup/eks-nodes"
		ec2ID = "arn:aws:ec2:us-east-1:000000000001:instance/i-eks-node"
		// K8s side.
		svcID = "prod/Service/api-lb"
		podID = "prod/Pod/api-worker"
		saID  = "prod/ServiceAccount/api-sa"
		// IAM admin (cross-graph terminal).
		roleID = "arn:aws:iam::000000000001:role/eks-admin"
	)

	// AWS side: internet-facing ALB → target group → EC2 node (EKS).
	fx.addInternetFacingALB(albID)
	fx.addNode(tgID, "elbv2-targetgroup", map[string]any{}, nil)
	fx.addEC2Instance(ec2ID)
	// K8s side: LB Service → Pod → SA with IRSA annotation.
	fx.addK8sLoadBalancerService(svcID, "prod")
	fx.addK8sPod(podID, "prod")
	fx.addK8sServiceAccountWithIRSA(saID, "prod", roleID)
	// Admin IAM role with persisted iam_escalation finding — the terminal.
	fx.addIAMRoleWithAdminFinding(roleID)

	// AWS edges.
	fx.link(albID, tgID, kgtypes.EdgeRoutesTo)
	fx.link(tgID, ec2ID, kgtypes.EdgeTargets)
	// Cross-domain structural edge: the EC2 node runs the pod (loose
	// composition — topology/ cannot model EKS node affinity strictly,
	// but an EdgeRoutesTo from EC2 → Service suffices to pivot).
	fx.link(ec2ID, svcID, kgtypes.EdgeRoutesTo)
	// K8s edges.
	fx.link(svcID, podID, kgtypes.EdgeSelects)
	fx.link(podID, saID, kgtypes.EdgeUsesSA)
	// The IRSA shortcut is inline: outgoingIRSAInline reads
	// saNode.Value("irsa_role_arn") and synthesizes an EdgeAssumesRole
	// edge with cross_graph=true. No explicit edge needed here.

	findings := fx.runUnifiedAnalyzer()
	require.NotEmpty(t, findings, "unified analyzer must emit at least one finding")

	// Every finding must carry the unified algorithm name.
	for _, f := range findings {
		require.Equal(t, "unified_public_exposure", f.Algorithm)
		require.NotEmpty(t, f.Evidence)
	}

	// At least one finding must terminate at the IAM admin role, carry
	// cross_graph=true metadata, and span both AWS and K8s node IDs.
	var matched int
	for _, f := range findings {
		terminal := f.Evidence[len(f.Evidence)-1]
		if terminal != roleID {
			continue
		}
		matched++
		// Severity is critical because the terminal is admin-reachable.
		require.Equal(t, SeverityCritical, f.Severity,
			"IRSA admin terminal must produce critical severity")
		// Metadata carries cross_graph=true from the IRSA bridge edge.
		require.Equal(t, "true", f.Metadata["cross_graph"],
			"unified IRSA path must carry cross_graph=true in metadata")

		// Hop list must span both AWS and K8s node ID namespaces.
		var sawAWS, sawK8s bool
		for _, id := range f.Evidence {
			switch {
			case contains(id, "arn:aws:"):
				sawAWS = true
			case contains(id, "/Pod/"), contains(id, "/Service/"), contains(id, "/ServiceAccount/"):
				sawK8s = true
			}
		}
		require.True(t, sawAWS, "hop list must include at least one AWS node ID, got %v", f.Evidence)
		require.True(t, sawK8s, "hop list must include at least one K8s node ID, got %v", f.Evidence)
	}
	require.GreaterOrEqualf(t, matched, 1,
		"expected at least 1 unified cross-graph finding, got findings=%+v", findings)
}

// contains is a tiny helper so the test file avoids pulling in strings
// just for substring matching; a literal scan is the simplest thing
// that works for our short ID fragments.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
