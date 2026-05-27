// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/require"
)

// public_exposure_sensitive_test.go exercises the sensitive-terminal
// classifier: the registry, dispatch, and each registered AWS/K8s rule.
// Tests build a minimal cloud graph via the shared cloudFixture and feed
// individual nodes to classifySensitive, asserting the (ok, score, reason)
// triple returned.
//
// The iam-role and ServiceAccount tests additionally create persisted
// iam_escalation findings in the knowledge graph to verify the
// finding-driven rules fire only when the linked role is admin-reachable.

const peTestAccount = "pe-sensitive-test"

// buildPESensitiveFixture returns a cloud fixture with one empty account
// ready for the per-rule tests to populate. Using a shared prefix keeps
// the tests isolated from other suites in the package.
func buildPESensitiveFixture(t *testing.T) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)
	fx.account(peTestAccount)
	return fx
}

// TestClassifySensitive_EmptyNodeType verifies a node with no resource_type
// metadata returns not-sensitive rather than panicking.
func TestClassifySensitive_EmptyNodeType(t *testing.T) {
	fx := buildPESensitiveFixture(t)
	scoped := fx.reader(peTestAccount)

	node := &knowledgev1.Node{Id: "bare", Type: string(kgtypes.NodeCloudResource)}
	ok, score, reason := classifySensitive(newTestCtx(t), scoped, fx, node)
	require.False(t, ok)
	require.InDelta(t, 0.0, score, 0.0001)
	require.Empty(t, reason)
}

// TestClassifySensitive_UnknownType verifies a resource_type with no
// registered rule returns not-sensitive.
func TestClassifySensitive_UnknownType(t *testing.T) {
	fx := buildPESensitiveFixture(t)
	scoped := fx.reader(peTestAccount)

	node := fx.AddCloudResource(peTestAccount, "arn:weird", "weird", "weird-unknown-type", nil)
	ok, _, _ := classifySensitive(newTestCtx(t), scoped, fx, node)
	require.False(t, ok)
}

// TestAWSSensitive_AlwaysSensitiveRules walks through every AWS type-based
// "always sensitive" rule and asserts they fire with their documented
// score and reason. If any rule regresses its score, this test catches it.
func TestAWSSensitive_AlwaysSensitiveRules(t *testing.T) {
	fx := buildPESensitiveFixture(t)
	scoped := fx.reader(peTestAccount)
	ctx := newTestCtx(t)

	tests := []struct {
		name         string
		resourceType string
		wantScore    float64
		wantReason   string
	}{
		{"rds", "rds-instance", 0.9, "relational database"},
		{"ddb", "dynamodb-table", 0.85, "DynamoDB table"},
		{"kms", "kms-key", 0.95, "KMS key"},
		{"secret", "secretsmanager-secret", 1.0, "secret"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := fx.AddCloudResource(peTestAccount, "arn:"+tc.name, tc.name, tc.resourceType, nil)
			ok, score, reason := classifySensitive(ctx, scoped, fx, n)
			require.True(t, ok, "%s must be classified as sensitive", tc.resourceType)
			require.InDelta(t, tc.wantScore, score, 0.0001)
			require.Equal(t, tc.wantReason, reason)
		})
	}
}

// TestAWSSensitive_IAMRoleRequiresFinding verifies an iam-role is NOT
// sensitive without a matching iam_escalation finding, and IS sensitive
// once the finding is present.
func TestAWSSensitive_IAMRoleRequiresFinding(t *testing.T) {
	fx := buildPESensitiveFixture(t)
	scoped := fx.reader(peTestAccount)
	ctx := newTestCtx(t)

	roleID := "arn:aws:iam::000000000001:role/admin"
	roleNode := fx.AddCloudResource(peTestAccount, roleID, "admin", "iam-role", nil)

	// No finding yet → role must not be flagged.
	ok, _, _ := classifySensitive(ctx, scoped, fx, roleNode)
	require.False(t, ok, "iam-role should not be sensitive without iam_escalation finding")

	// Seed a topology:iam_escalation finding with this role as the primary
	// evidence. The knowledge graph is the same composite the scoped DB
	// reads via Knowledge().
	fx.AddKnowledgeFinding("finding:admin", "iam_escalation", roleID)

	// With the finding in place, the role IS sensitive.
	ok, score, reason := classifySensitive(ctx, scoped, fx, roleNode)
	require.True(t, ok, "iam-role should be sensitive once iam_escalation finding exists")
	require.InDelta(t, 1.0, score, 0.0001)
	require.Equal(t, "admin-reachable IAM role", reason)
}

// TestK8sSensitive_SecretAlways verifies every Kubernetes Secret is
// classified as sensitive without additional context.
func TestK8sSensitive_SecretAlways(t *testing.T) {
	fx := buildPESensitiveFixture(t)
	scoped := fx.reader(peTestAccount)

	node := fx.AddCloudResource(peTestAccount, "k8s://default/Secret/api-keys", "api-keys", "Secret", nil)
	ok, score, reason := classifySensitive(newTestCtx(t), scoped, fx, node)
	require.True(t, ok)
	require.InDelta(t, 0.9, score, 0.0001)
	require.Equal(t, "Kubernetes secret", reason)
}

// TestK8sSensitive_ServiceAccountNoIRSA verifies a ServiceAccount without
// an irsa_role_arn metadata key is NOT sensitive.
func TestK8sSensitive_ServiceAccountNoIRSA(t *testing.T) {
	fx := buildPESensitiveFixture(t)
	scoped := fx.reader(peTestAccount)

	node := fx.AddCloudResource(peTestAccount, "k8s://default/ServiceAccount/regular", "regular", "ServiceAccount", nil)
	ok, _, _ := classifySensitive(newTestCtx(t), scoped, fx, node)
	require.False(t, ok, "plain ServiceAccount should not be sensitive")
}

// TestK8sSensitive_ServiceAccountIRSAWithoutFinding verifies a
// ServiceAccount that bears an IRSA annotation but whose role has no
// iam_escalation finding is NOT sensitive.
func TestK8sSensitive_ServiceAccountIRSAWithoutFinding(t *testing.T) {
	fx := buildPESensitiveFixture(t)
	scoped := fx.reader(peTestAccount)

	node := fx.AddCloudResource(peTestAccount,
		"k8s://default/ServiceAccount/app",
		"app",
		"ServiceAccount",
		map[string]string{"irsa_role_arn": "arn:aws:iam::000000000001:role/ops"},
	)
	ok, _, _ := classifySensitive(newTestCtx(t), scoped, fx, node)
	require.False(t, ok, "IRSA-only ServiceAccount must not be sensitive without an iam_escalation finding")
}

// TestK8sSensitive_ServiceAccountIRSAAdmin verifies a ServiceAccount with
// both an IRSA annotation AND a matching iam_escalation finding IS
// sensitive with the IRSA admin reason.
func TestK8sSensitive_ServiceAccountIRSAAdmin(t *testing.T) {
	fx := buildPESensitiveFixture(t)
	scoped := fx.reader(peTestAccount)
	ctx := newTestCtx(t)

	roleARN := "arn:aws:iam::000000000001:role/eks-admin"
	saNode := fx.AddCloudResource(peTestAccount,
		"k8s://prod/ServiceAccount/deployer",
		"deployer",
		"ServiceAccount",
		map[string]string{"irsa_role_arn": roleARN},
	)

	// Persist the matching iam_escalation finding.
	fx.AddKnowledgeFinding("finding:eks-admin", "iam_escalation", roleARN)

	ok, score, reason := classifySensitive(ctx, scoped, fx, saNode)
	require.True(t, ok)
	require.InDelta(t, 1.0, score, 0.0001)
	require.Equal(t, "IRSA-bound admin IAM role", reason)
}

// TestRegisterSensitiveRule_DuplicatePanics verifies that registering the
// same resource_type twice panics, matching the programmer-error convention
// of the other registries in this package.
func TestRegisterSensitiveRule_DuplicatePanics(t *testing.T) {
	// Isolate the registry so we don't clobber the production rules.
	defer func() {
		// Reinstall production rules by re-running the init functions is
		// impossible (Go inits run only once). Instead, reset the registry
		// and manually re-register every rule the sibling files declared
		// so later tests still see the production set.
		resetSensitiveRegistryForTest()
		registerSensitiveRule("rds-instance", awsRDSInstanceSensitive)
		registerSensitiveRule("dynamodb-table", awsDynamoDBTableSensitive)
		registerSensitiveRule("kms-key", awsKMSKeySensitive)
		registerSensitiveRule("secretsmanager-secret", awsSecretsManagerSecretSensitive)
		registerSensitiveRule("iam-role", awsIAMRoleSensitive)
		registerSensitiveRule("Secret", k8sSecretSensitive)
		registerSensitiveRule("ServiceAccount", k8sServiceAccountSensitive)
	}()
	resetSensitiveRegistryForTest()
	registerSensitiveRule("test-dup", awsRDSInstanceSensitive)
	require.Panics(t, func() {
		registerSensitiveRule("test-dup", awsRDSInstanceSensitive)
	})
}

// TestRegisterSensitiveRule_NilPanics verifies a nil rule panics.
func TestRegisterSensitiveRule_NilPanics(t *testing.T) {
	require.Panics(t, func() {
		registerSensitiveRule("test-nil", nil)
	})
}

// TestRegisterSensitiveRule_EmptyTypePanics verifies an empty resource_type
// panics.
func TestRegisterSensitiveRule_EmptyTypePanics(t *testing.T) {
	require.Panics(t, func() {
		registerSensitiveRule("", awsRDSInstanceSensitive)
	})
}
