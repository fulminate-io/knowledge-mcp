// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_sensitive_k8s.go holds the Kubernetes-specific sensitive-
// terminal rules for the public_exposure analyzer family.
//
// Rules (type-based only, per plan OQ5):
//
//   - Secret (every Kubernetes Secret)          — always sensitive,   0.9
//   - ServiceAccount with IRSA-admin annotation — context-dependent,  1.0
//
// ServiceAccount is not ALWAYS sensitive — every pod has one — but a pod-
// level SA that holds an admin-reachable IRSA IAM role IS a terminal
// because compromising the pod trivially promotes to the cloud side. The
// rule consumes the "irsa_role_arn" metadata the K8s collector sets on
// ServiceAccount nodes (see cloud/k8s/sub_serviceaccounts.go), cross-refs
// the persisted iam_escalation finding set, and flags the SA when the
// role has a finding.

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// init self-registers the K8s sensitive-terminal rules with the shared
// classifier. Called once at package load; duplicate registration panics
// via registerSensitiveRule.
func init() {
	registerSensitiveRule("Secret", k8sSecretSensitive)
	registerSensitiveRule("ServiceAccount", k8sServiceAccountSensitive)
}

// k8sSecretSensitive flags every Kubernetes Secret as a sensitive terminal.
// Score 0.9 — Kubernetes Secrets are the cluster equivalent of Secrets
// Manager entries; reaching one from the public internet is a direct data
// exfiltration path. We stay one tick below the AWS Secrets Manager score
// (1.0) because K8s Secrets are often short-lived tokens or config values
// rather than long-lived API keys, but the distinction is soft.
func k8sSecretSensitive(_ context.Context, _ *cloudReader, _ foundation.GraphCaller, _ *knowledgev1.Node) (bool, float64, string) {
	return true, 0.9, "Kubernetes secret"
}

// k8sServiceAccountSensitive flags a ServiceAccount as a sensitive terminal
// when:
//
//  1. the SA has an "irsa_role_arn" metadata value populated by the K8s
//     collector (i.e. it participates in the IRSA workload-identity
//     pattern), AND
//  2. that IAM role ARN matches a persisted iam_escalation finding (i.e.
//     the role is already known to be admin-reachable by the existing
//     iam_escalation analyzer).
//
// If either condition is false the SA is not sensitive. Score 1.0 when
// both match — compromising a pod that bears this SA is a direct promote
// to "effective admin in the AWS account", which is as bad as terminating
// at the IAM role itself.
func k8sServiceAccountSensitive(ctx context.Context, _ *cloudReader, rootCaller foundation.GraphCaller, node *knowledgev1.Node) (bool, float64, string) {
	roleARN := nodeMeta(node, "irsa_role_arn")
	if roleARN == "" {
		return false, 0, ""
	}
	if !iamRoleHasEscalationFinding(ctx, rootCaller, roleARN) {
		return false, 0, ""
	}
	return true, 1.0, "IRSA-bound admin IAM role"
}
