// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_sensitive_aws.go holds the AWS-specific sensitive-terminal
// rules registered with the shared sensitive classifier in
// public_exposure_sensitive.go.
//
// All rules are STRICTLY type-based (per plan OQ5 decision — no name regex,
// no tag matching, no PII/prod heuristics). The one exception that isn't
// trivially "always sensitive" is iam-role: an IAM role is only flagged as
// a sensitive terminal when the persisted iam_escalation analyzer has
// already produced a finding that targets it. That removes every false
// positive on non-admin operational roles (lambda-exec, ec2-describe,
// appmesh-envoy) that would otherwise get scored as "critical".
//
// The roles-via-finding lookup is intentionally cheap: the classifier
// loads all iam_escalation findings once per walker run in the walker's
// iamAdminTerminalReached helper (see public_exposure_walk.go). This file
// only declares the rules; the helper does the caching.

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// init self-registers the AWS sensitive-terminal rules with the shared
// classifier. Called once at package load; duplicate registration panics
// via registerSensitiveRule.
func init() {
	registerSensitiveRule("rds-instance", awsRDSInstanceSensitive)
	registerSensitiveRule("dynamodb-table", awsDynamoDBTableSensitive)
	registerSensitiveRule("kms-key", awsKMSKeySensitive)
	registerSensitiveRule("secretsmanager-secret", awsSecretsManagerSecretSensitive)
	registerSensitiveRule("iam-role", awsIAMRoleSensitive)
}

// awsRDSInstanceSensitive flags every RDS DB instance as a sensitive
// terminal. Score 0.9 — reaching a relational database from the public
// internet is always a serious incident even if auth is still required on
// the DB itself, because network-layer access lifts the attacker above
// "no access" into "actively probing the DB engine".
func awsRDSInstanceSensitive(_ context.Context, _ *cloudReader, _ foundation.GraphCaller, _ *knowledgev1.Node) (bool, float64, string) {
	return true, 0.9, "relational database"
}

// awsDynamoDBTableSensitive flags every DynamoDB table. DynamoDB is IAM-
// auth'd, so network reachability alone is less severe than RDS, but
// compromised credentials in a public-facing workload make the table
// directly reachable — score 0.85.
func awsDynamoDBTableSensitive(_ context.Context, _ *cloudReader, _ foundation.GraphCaller, _ *knowledgev1.Node) (bool, float64, string) {
	return true, 0.85, "DynamoDB table"
}

// awsKMSKeySensitive flags every KMS key. KMS is the root of AWS
// encryption — reaching a key with decrypt permissions means reading
// every encrypted volume, bucket, and field-level-encrypted column that
// key protects. Score 0.95.
func awsKMSKeySensitive(_ context.Context, _ *cloudReader, _ foundation.GraphCaller, _ *knowledgev1.Node) (bool, float64, string) {
	return true, 0.95, "KMS key"
}

// awsSecretsManagerSecretSensitive flags every Secrets Manager secret.
// Score 1.0 — the whole point of Secrets Manager is to hold things that
// must never be reachable from untrusted networks. If a path terminates at
// a secret, this is as bad as it gets.
func awsSecretsManagerSecretSensitive(_ context.Context, _ *cloudReader, _ foundation.GraphCaller, _ *knowledgev1.Node) (bool, float64, string) {
	return true, 1.0, "secret"
}

// awsIAMRoleSensitive is the finding-driven rule: an IAM role is a
// sensitive terminal if and only if a persisted iam_escalation finding
// with this role's ID as primary_evidence already exists on the knowledge
// graph. This defers the expensive "is this role admin-reachable"
// computation to the existing iam_escalation analyzer and consumes only
// its persisted output, as the plan rule requires.
//
// The lookup goes through the walker's iamAdminTerminalReached helper so
// the set of admin-reachable roles is cached across the walker run. At
// classifier time we don't have access to a per-run cache; we instead
// re-use the unscoped wire caller and query the knowledge graph directly.
// The walker also calls this helper when evaluating the sensitive-terminal
// check, so the double-lookup is amortized by the one-shot query pattern
// inside iamAdminTerminalReached.
func awsIAMRoleSensitive(ctx context.Context, _ *cloudReader, rootCaller foundation.GraphCaller, node *knowledgev1.Node) (bool, float64, string) {
	if node.Id == "" {
		return false, 0, ""
	}
	if iamRoleHasEscalationFinding(ctx, rootCaller, node.Id) {
		return true, 1.0, "admin-reachable IAM role"
	}
	return false, 0, ""
}

// iamRoleHasEscalationFinding returns true if the knowledge graph contains
// a topology:iam_escalation finding whose primary_evidence metadata matches
// the given role ID. The lookup is a meta-filtered knowledge-findings wire
// read (foundation.FetchKnowledgeFindings) — the wire helper always
// addresses the real knowledge graph, so there is no scoped-vs-root hazard
// like the legacy compositeDB.Knowledge() routing carried.
//
// Returns false fast on any error or when rootCaller is nil: a missing
// knowledge graph, a permission hiccup, or a reduced-context test caller
// should never cause the public_exposure analyzer to crash. False
// negatives here just mean we lose the "IAM-role-as-terminal" enrichment —
// paths to those roles still get scored at their default seed-level
// severity.
func iamRoleHasEscalationFinding(ctx context.Context, rootCaller foundation.GraphCaller, roleID string) bool {
	if rootCaller == nil || roleID == "" {
		return false
	}
	nodes, err := foundation.FetchKnowledgeFindings(ctx, rootCaller, "iam_escalation", roleID)
	if err != nil {
		return false
	}
	return len(nodes) > 0
}
