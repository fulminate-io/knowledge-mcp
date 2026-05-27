// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_seeds_aws.go holds the AWS seed rules that detect
// public-entry resources by re-parsing the cloud collector's node.Content
// envelopes. Each rule uses a LOCAL anonymous struct for parsing so we
// avoid importing cloud/aws — the only contract between this file and the
// collector is the resource_type strings and the JSON shapes documented
// in the Phase 1 seed catalog finding.
//
// Rules (cloud family "aws"):
//
//   - elbv2-loadbalancer     — Scheme=internet-facing, score 0.9
//   - lambda-function        — function_url_config.auth_type=NONE, score 1.0
//   - s3-bucket              — PAB disabled OR policy public OR ACL public,
//                              score scales with how public it is
//   - ec2-instance           — PublicIpAddress non-empty, score 0.5
//   - rds-instance           — PubliclyAccessible=true, score 0.7
//   - apigw:restapi          — any method auth=NONE, score 0.9
//   - apigw:httpapi          — any route auth=NONE, score 0.9
//   - apigw:wsapi            — any route auth=NONE, score 0.9
//
// Per Phase 1 OQ: CloudFront is intentionally deferred — no collector
// exists yet for CloudFront distributions, so a rule here would never
// match. Tracked in the seed catalog finding as a follow-up.

import (
	"context"
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func init() {
	registerSeedRule("elbv2-loadbalancer", "aws", awsELBv2SeedRule)
	registerSeedRule("lambda-function", "aws", awsLambdaSeedRule)
	registerSeedRule("s3-bucket", "aws", awsS3SeedRule)
	registerSeedRule("ec2-instance", "aws", awsEC2SeedRule)
	registerSeedRule("rds-instance", "aws", awsRDSSeedRule)
	registerSeedRule("apigw:restapi", "aws", awsAPIGatewayV1SeedRule)
	registerSeedRule("apigw:httpapi", "aws", awsAPIGatewayV2SeedRule)
	registerSeedRule("apigw:wsapi", "aws", awsAPIGatewayV2SeedRule)
}

// awsELBv2SeedRule fires when the load balancer's Scheme field is
// "internet-facing" (the ELBv2 SDK value; "internal" LBs are not public).
// The local struct mirrors the collector's raw json.Marshal of
// elbv2types.LoadBalancer.
func awsELBv2SeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	var lb struct {
		Scheme string `json:"Scheme"`
	}
	if err := json.Unmarshal([]byte(node.Content), &lb); err != nil {
		return nil, nil //nolint:nilerr // parse errors = not-a-seed per seedRule contract
	}
	if lb.Scheme != "internet-facing" {
		return nil, nil
	}
	return &publicSeed{
		NodeID:     node.Id,
		EntryScore: 0.9,
		Reason:     "internet-facing load balancer",
	}, nil
}

// awsLambdaSeedRule fires when the Lambda envelope carries a
// function_url_config with auth_type=NONE. Envelope shape defined in
// cloud/aws/lambda.go lambdaFunctionContent.
func awsLambdaSeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	var env struct {
		FunctionURLConfig *struct {
			AuthType string `json:"auth_type"`
		} `json:"function_url_config"`
	}
	if err := json.Unmarshal([]byte(node.Content), &env); err != nil {
		return nil, nil //nolint:nilerr
	}
	if env.FunctionURLConfig == nil || env.FunctionURLConfig.AuthType != "NONE" {
		return nil, nil
	}
	return &publicSeed{
		NodeID:     node.Id,
		EntryScore: 1.0,
		Reason:     "Lambda function URL with AUTH_TYPE=NONE",
	}, nil
}

// awsS3SeedRule fires when the bucket is public via any of three paths:
// 1) PublicAccessBlock is missing or has at least one of the "allow public"
// flags false; 2) bucket_policy_status.is_public=true; 3) any ACL grant
// targets the AllUsers or AuthenticatedUsers group URI. Score scales with
// severity: ACL grant is worst (0.95), followed by public policy (0.85),
// followed by PAB-disabled-but-no-policy-attached (0.7).
func awsS3SeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	var env struct {
		PublicAccessBlock *struct {
			BlockPublicAcls       bool `json:"block_public_acls"`
			BlockPublicPolicy     bool `json:"block_public_policy"`
			IgnorePublicAcls      bool `json:"ignore_public_acls"`
			RestrictPublicBuckets bool `json:"restrict_public_buckets"`
		} `json:"public_access_block"`
		PublicAccessBlockMissing bool `json:"public_access_block_missing"`
		BucketPolicyStatus       *struct {
			IsPublic bool `json:"is_public"`
		} `json:"bucket_policy_status"`
		ACLPublicGrants []struct {
			GroupURI   string `json:"group_uri"`
			Permission string `json:"permission"`
		} `json:"acl_public_grants"`
	}
	if err := json.Unmarshal([]byte(node.Content), &env); err != nil {
		return nil, nil //nolint:nilerr
	}

	// Worst case: an explicit public ACL grant.
	if len(env.ACLPublicGrants) > 0 {
		return &publicSeed{
			NodeID:     node.Id,
			EntryScore: 0.95,
			Reason:     "S3 bucket ACL grants public access",
		}, nil
	}
	// Next-worst: public bucket policy.
	if env.BucketPolicyStatus != nil && env.BucketPolicyStatus.IsPublic {
		return &publicSeed{
			NodeID:     node.Id,
			EntryScore: 0.85,
			Reason:     "S3 bucket policy evaluates as public",
		}, nil
	}
	// PAB disabled = potential public; lower score because no policy/ACL
	// has actually granted access yet. PAB-missing is treated the same way.
	if env.PublicAccessBlockMissing || awsS3PABAllowsPublic(env.PublicAccessBlock) {
		return &publicSeed{
			NodeID:     node.Id,
			EntryScore: 0.7,
			Reason:     "S3 public access block permits future public grants",
		}, nil
	}
	return nil, nil
}

// awsS3PABAllowsPublic returns true if the four-flag PAB struct has any
// flag cleared (false = allows public). When all four are true the bucket
// is locked down. nil is treated as "all flags false" — i.e. a missing
// PAB is fully permissive, which matches the AWS default for older
// buckets.
func awsS3PABAllowsPublic(pab *struct {
	BlockPublicAcls       bool `json:"block_public_acls"`
	BlockPublicPolicy     bool `json:"block_public_policy"`
	IgnorePublicAcls      bool `json:"ignore_public_acls"`
	RestrictPublicBuckets bool `json:"restrict_public_buckets"`
}) bool {
	if pab == nil {
		return true
	}
	return !pab.BlockPublicAcls ||
		!pab.BlockPublicPolicy ||
		!pab.IgnorePublicAcls ||
		!pab.RestrictPublicBuckets
}

// awsEC2SeedRule fires when the EC2 instance has a non-empty
// PublicIpAddress. Note: having a public IP alone isn't enough to reach
// the instance — an SG rule must also allow inbound — so the entry score
// is set to 0.5, acknowledging "on the public internet" without asserting
// "actively reachable". The walker will prune down further by evaluating
// SG ingress rules downstream.
func awsEC2SeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	var instance struct {
		PublicIPAddress string `json:"PublicIpAddress"`
	}
	if err := json.Unmarshal([]byte(node.Content), &instance); err != nil {
		return nil, nil //nolint:nilerr
	}
	if instance.PublicIPAddress == "" {
		return nil, nil
	}
	return &publicSeed{
		NodeID:     node.Id,
		EntryScore: 0.5,
		Reason:     "EC2 instance has a public IP address",
	}, nil
}

// awsRDSSeedRule fires when the RDS instance has PubliclyAccessible=true.
// Score 0.7 — the SG still has to permit inbound, but RDS specifically
// lives in a subnet routable from the public internet when this flag is
// set, which is a significant step down the exposure curve.
func awsRDSSeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	var instance struct {
		PubliclyAccessible bool `json:"PubliclyAccessible"`
	}
	if err := json.Unmarshal([]byte(node.Content), &instance); err != nil {
		return nil, nil //nolint:nilerr
	}
	if !instance.PubliclyAccessible {
		return nil, nil
	}
	return &publicSeed{
		NodeID:     node.Id,
		EntryScore: 0.7,
		Reason:     "RDS instance has PubliclyAccessible=true",
	}, nil
}

// awsAPIGatewayV1SeedRule fires when a REST API has at least one method
// with authorization_type=NONE. Envelope shape from
// cloud/aws/apigateway.go restAPIContent.
func awsAPIGatewayV1SeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	var env struct {
		Methods []struct {
			AuthorizationType string `json:"authorization_type"`
		} `json:"methods"`
	}
	if err := json.Unmarshal([]byte(node.Content), &env); err != nil {
		return nil, nil //nolint:nilerr
	}
	for _, m := range env.Methods {
		if m.AuthorizationType == "NONE" {
			return &publicSeed{
				NodeID:     node.Id,
				EntryScore: 0.9,
				Reason:     "API Gateway v1 REST API has a method with authorization NONE",
			}, nil
		}
	}
	return nil, nil
}

// awsAPIGatewayV2SeedRule fires when an HTTP or WebSocket v2 API has at
// least one route with authorization_type=NONE. Envelope from
// cloud/aws/apigatewayv2.go v2APIContent.
func awsAPIGatewayV2SeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	var env struct {
		Routes []struct {
			AuthorizationType string `json:"authorization_type"`
		} `json:"routes"`
	}
	if err := json.Unmarshal([]byte(node.Content), &env); err != nil {
		return nil, nil //nolint:nilerr
	}
	for _, r := range env.Routes {
		if r.AuthorizationType == "NONE" {
			return &publicSeed{
				NodeID:     node.Id,
				EntryScore: 0.9,
				Reason:     "API Gateway v2 API has a route with authorization NONE",
			}, nil
		}
	}
	return nil, nil
}
