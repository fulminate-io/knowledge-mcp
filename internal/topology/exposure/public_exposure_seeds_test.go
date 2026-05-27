// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// public_exposure_seeds_test.go exercises every registered AWS/K8s seed
// rule and the enumerateSeeds dispatcher.

const peSeedsAccount = "pe-seeds-test"

func buildPESeedsFixture(t *testing.T) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)
	fx.account(peSeedsAccount)
	return fx
}

// seedFromFixture marshals a content value as JSON and creates a cloud
// resource node with the given resource_type. Tests assert on the seed's
// NodeID via the original id argument they passed in.
func seedFromFixture(t *testing.T, fx *cloudFixture, id, resourceType string, content any) {
	t.Helper()
	body, err := json.Marshal(content)
	require.NoError(t, err)
	fx.AddCloudResourceWithContent(peSeedsAccount, id, id, resourceType, string(body), nil)
}

// TestAWSSeedRules_ELBv2 verifies the ELBv2 seed fires on internet-facing
// scheme and not on internal scheme.
func TestAWSSeedRules_ELBv2(t *testing.T) {
	fx := buildPESeedsFixture(t)
	seedFromFixture(t, fx, "arn:alb:public", "elbv2-loadbalancer",
		map[string]any{"Scheme": "internet-facing"})
	seedFromFixture(t, fx, "arn:alb:internal", "elbv2-loadbalancer",
		map[string]any{"Scheme": "internal"})

	scoped := fx.reader(peSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "aws")

	require.Len(t, seeds, 1)
	require.Equal(t, "arn:alb:public", seeds[0].NodeID)
	require.InDelta(t, 0.9, seeds[0].EntryScore, 0.0001)
	require.Contains(t, seeds[0].Reason, "internet-facing")
}

// TestAWSSeedRules_Lambda verifies the Lambda seed fires for AUTH_TYPE=NONE
// and not for AWS_IAM.
func TestAWSSeedRules_Lambda(t *testing.T) {
	fx := buildPESeedsFixture(t)
	seedFromFixture(t, fx, "arn:lambda:open", "lambda-function",
		map[string]any{"function_url_config": map[string]any{"auth_type": "NONE"}})
	seedFromFixture(t, fx, "arn:lambda:iam", "lambda-function",
		map[string]any{"function_url_config": map[string]any{"auth_type": "AWS_IAM"}})
	seedFromFixture(t, fx, "arn:lambda:no-url", "lambda-function",
		map[string]any{"function": map[string]any{}})

	scoped := fx.reader(peSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "aws")
	require.Len(t, seeds, 1)
	require.Equal(t, "arn:lambda:open", seeds[0].NodeID)
	require.InDelta(t, 1.0, seeds[0].EntryScore, 0.0001)
}

// TestAWSSeedRules_S3 covers the three public paths (ACL, policy, PAB)
// plus the locked-down negative case.
func TestAWSSeedRules_S3(t *testing.T) {
	fx := buildPESeedsFixture(t)
	seedFromFixture(t, fx, "arn:s3:acl", "s3-bucket", map[string]any{
		"acl_public_grants": []map[string]any{{"group_uri": "http://acs.amazonaws.com/groups/global/AllUsers", "permission": "READ"}},
	})
	seedFromFixture(t, fx, "arn:s3:policy", "s3-bucket", map[string]any{
		"bucket_policy_status": map[string]any{"is_public": true},
		"public_access_block": map[string]any{
			"block_public_acls": true, "block_public_policy": true,
			"ignore_public_acls": true, "restrict_public_buckets": true,
		},
	})
	seedFromFixture(t, fx, "arn:s3:pab-missing", "s3-bucket", map[string]any{
		"public_access_block_missing": true,
	})
	seedFromFixture(t, fx, "arn:s3:locked", "s3-bucket", map[string]any{
		"public_access_block": map[string]any{
			"block_public_acls": true, "block_public_policy": true,
			"ignore_public_acls": true, "restrict_public_buckets": true,
		},
	})

	scoped := fx.reader(peSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "aws")

	// 3 public, 1 locked.
	require.Len(t, seeds, 3)
	byID := map[string]publicSeed{}
	for _, s := range seeds {
		byID[s.NodeID] = s
	}
	require.InDelta(t, 0.95, byID["arn:s3:acl"].EntryScore, 0.0001)
	require.InDelta(t, 0.85, byID["arn:s3:policy"].EntryScore, 0.0001)
	require.InDelta(t, 0.7, byID["arn:s3:pab-missing"].EntryScore, 0.0001)
}

// TestAWSSeedRules_EC2 verifies the EC2 seed fires for instances with a
// public IP and not for private-only instances.
func TestAWSSeedRules_EC2(t *testing.T) {
	fx := buildPESeedsFixture(t)
	seedFromFixture(t, fx, "arn:ec2:public", "ec2-instance",
		map[string]any{"PublicIpAddress": "54.1.2.3"})
	seedFromFixture(t, fx, "arn:ec2:private", "ec2-instance",
		map[string]any{"PublicIpAddress": ""})

	scoped := fx.reader(peSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "aws")
	require.Len(t, seeds, 1)
	require.Equal(t, "arn:ec2:public", seeds[0].NodeID)
	require.InDelta(t, 0.5, seeds[0].EntryScore, 0.0001)
}

// TestAWSSeedRules_RDS verifies the PubliclyAccessible flag drives the
// RDS seed.
func TestAWSSeedRules_RDS(t *testing.T) {
	fx := buildPESeedsFixture(t)
	seedFromFixture(t, fx, "arn:rds:public", "rds-instance",
		map[string]any{"PubliclyAccessible": true})
	seedFromFixture(t, fx, "arn:rds:private", "rds-instance",
		map[string]any{"PubliclyAccessible": false})

	scoped := fx.reader(peSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "aws")
	require.Len(t, seeds, 1)
	require.Equal(t, "arn:rds:public", seeds[0].NodeID)
	require.InDelta(t, 0.7, seeds[0].EntryScore, 0.0001)
}

// TestAWSSeedRules_APIGateway covers v1 + v2 APIs with mixed auth types.
func TestAWSSeedRules_APIGateway(t *testing.T) {
	fx := buildPESeedsFixture(t)
	seedFromFixture(t, fx, "arn:apigw:v1-open", "apigw:restapi", map[string]any{
		"methods": []map[string]any{
			{"authorization_type": "AWS_IAM"},
			{"authorization_type": "NONE"},
		},
	})
	seedFromFixture(t, fx, "arn:apigw:v1-locked", "apigw:restapi", map[string]any{
		"methods": []map[string]any{{"authorization_type": "AWS_IAM"}},
	})
	seedFromFixture(t, fx, "arn:apigw:v2-http-open", "apigw:httpapi", map[string]any{
		"routes": []map[string]any{{"authorization_type": "NONE"}},
	})
	seedFromFixture(t, fx, "arn:apigw:v2-ws-open", "apigw:wsapi", map[string]any{
		"routes": []map[string]any{{"authorization_type": "NONE"}},
	})

	scoped := fx.reader(peSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "aws")
	require.Len(t, seeds, 3) // v1-open, v2-http-open, v2-ws-open; v1-locked excluded
	for _, s := range seeds {
		require.InDelta(t, 0.9, s.EntryScore, 0.0001)
	}
}

// TestK8sSeedRules_Service verifies the Service seed fires for
// LoadBalancer and not for ClusterIP.
func TestK8sSeedRules_Service(t *testing.T) {
	fx := buildPESeedsFixture(t)

	// Populate metadata["type"] to match the collector's flattened form.
	fx.AddCloudResource(peSeedsAccount, "k8s:svc:lb", "lb", "Service", map[string]string{
		"type": "LoadBalancer",
	})
	fx.AddCloudResource(peSeedsAccount, "k8s:svc:internal", "internal", "Service", map[string]string{
		"type": "ClusterIP",
	})

	scoped := fx.reader(peSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "k8s")
	require.Len(t, seeds, 1)
	require.Equal(t, "k8s:svc:lb", seeds[0].NodeID)
	require.InDelta(t, 0.9, seeds[0].EntryScore, 0.0001)
	require.Equal(t, "k8s", seeds[0].CloudFamily)
}

// TestK8sSeedRules_Ingress verifies every Ingress is flagged.
func TestK8sSeedRules_Ingress(t *testing.T) {
	fx := buildPESeedsFixture(t)
	seedFromFixture(t, fx, "k8s:ing:api", "Ingress", map[string]any{})

	scoped := fx.reader(peSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "k8s")
	require.Len(t, seeds, 1)
	require.InDelta(t, 0.8, seeds[0].EntryScore, 0.0001)
}

// TestEnumerateSeeds_CloudFilter verifies the cloudFilter parameter
// correctly scopes output to one family.
func TestEnumerateSeeds_CloudFilter(t *testing.T) {
	fx := buildPESeedsFixture(t)
	seedFromFixture(t, fx, "arn:alb:public", "elbv2-loadbalancer",
		map[string]any{"Scheme": "internet-facing"})
	seedFromFixture(t, fx, "k8s:ing:api", "Ingress", map[string]any{})

	scoped := fx.reader(peSeedsAccount)

	all := enumerateSeeds(newTestCtx(t), scoped, "")
	require.Len(t, all, 2)

	awsOnly := enumerateSeeds(newTestCtx(t), scoped, "aws")
	require.Len(t, awsOnly, 1)
	require.Equal(t, "arn:alb:public", awsOnly[0].NodeID)

	k8sOnly := enumerateSeeds(newTestCtx(t), scoped, "k8s")
	require.Len(t, k8sOnly, 1)
	require.Equal(t, "k8s:ing:api", k8sOnly[0].NodeID)
}

// TestRegisterSeedRule_DuplicatePanics verifies double registration
// panics.
func TestRegisterSeedRule_DuplicatePanics(t *testing.T) {
	defer func() {
		resetSeedRegistryForTest()
		registerSeedRule("elbv2-loadbalancer", "aws", awsELBv2SeedRule)
		registerSeedRule("lambda-function", "aws", awsLambdaSeedRule)
		registerSeedRule("s3-bucket", "aws", awsS3SeedRule)
		registerSeedRule("ec2-instance", "aws", awsEC2SeedRule)
		registerSeedRule("rds-instance", "aws", awsRDSSeedRule)
		registerSeedRule("apigw:restapi", "aws", awsAPIGatewayV1SeedRule)
		registerSeedRule("apigw:httpapi", "aws", awsAPIGatewayV2SeedRule)
		registerSeedRule("apigw:wsapi", "aws", awsAPIGatewayV2SeedRule)
		registerSeedRule("Service", "k8s", k8sServiceSeedRule)
		registerSeedRule("Ingress", "k8s", k8sIngressSeedRule)
		registerSeedRule("Microsoft.Network/applicationGateways", "azure", azureAppGatewaySeedRule)
		registerSeedRule("Microsoft.Cdn/profiles/afdEndpoints", "azure", azureFrontDoorEndpointSeedRule)
		registerSeedRule("gcp:compute:forwardingRule", "gcp", gcpForwardingRuleSeedRule)
		registerSeedRule("gcp:compute:securityPolicy", "gcp", gcpCloudArmorSeedRule)
	}()
	resetSeedRegistryForTest()
	registerSeedRule("test-dup", "aws", awsELBv2SeedRule)
	require.Panics(t, func() {
		registerSeedRule("test-dup", "aws", awsELBv2SeedRule)
	})
}

// TestRegisterSeedRule_BadFamilyPanics verifies an invalid cloudFamily
// panics.
func TestRegisterSeedRule_BadFamilyPanics(t *testing.T) {
	require.Panics(t, func() {
		registerSeedRule("test-bad", "invalid", awsELBv2SeedRule)
	})
}
