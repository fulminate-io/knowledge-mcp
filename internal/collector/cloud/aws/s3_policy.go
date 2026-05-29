// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectEncryptionEdges calls GetBucketEncryption and emits one
// EdgeEncryptsWith edge per SSE-KMS default rule. Missing SSE config is
// treated as "no encryption edges" (fail-open, no error). The returned
// slice is nil when the bucket has no default encryption or is SSE-S3
// rather than SSE-KMS.
func (c *s3Collector) collectEncryptionEdges(ctx context.Context, bucketARN, bucketName string) []cloud.EdgeSpec {
	out, err := c.client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{
		Bucket: awssdk.String(bucketName),
	})
	if err != nil {
		if isNoSuchBucketEncryption(err) {
			return nil
		}
		slog.Warn("s3: get bucket encryption", "bucket", bucketName, "error", err)
		return nil
	}
	if out == nil || out.ServerSideEncryptionConfiguration == nil {
		return nil
	}

	var edges []cloud.EdgeSpec
	for _, rule := range out.ServerSideEncryptionConfiguration.Rules {
		def := rule.ApplyServerSideEncryptionByDefault
		if def == nil {
			continue
		}
		if def.SSEAlgorithm != s3types.ServerSideEncryptionAwsKms {
			continue
		}
		// KMSMasterKeyID may be a key ID, alias, or full ARN per the S3 API.
		// Canonicalize through resolveKMSKeyARN so the edge target matches
		// the KMS subcollector's node ID format. Same shape as ECR/RDS/etc.
		kmsKey := awssdk.ToString(def.KMSMasterKeyID)
		if kmsKey == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     bucketARN,
			TargetID:     resolveKMSKeyARN(kmsKey, c.region, c.accountID),
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}
	return edges
}

// collectPolicyGrantEdges calls GetBucketPolicy, parses the JSON document,
// and emits one EdgeGrants edge per (statement, principal) pair. Any
// Condition block on the statement is serialized as compact JSON and
// attached to the edge Metadata under key "condition". Missing policy
// (NoSuchBucketPolicy) is not an error.
func (c *s3Collector) collectPolicyGrantEdges(ctx context.Context, bucketARN, bucketName string) []cloud.EdgeSpec {
	out, err := c.client.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{
		Bucket: awssdk.String(bucketName),
	})
	if err != nil {
		if isNoSuchBucketPolicy(err) {
			return nil
		}
		slog.Warn("s3: get bucket policy", "bucket", bucketName, "error", err)
		return nil
	}
	if out == nil || out.Policy == nil {
		return nil
	}

	edges, perr := parseBucketPolicy(bucketARN, awssdk.ToString(out.Policy))
	if perr != nil {
		slog.Warn("s3: parse bucket policy", "bucket", bucketName, "error", perr)
		return nil
	}
	return edges
}

// bucketPolicyDoc mirrors the AWS IAM policy document JSON shape we care
// about for S3 bucket policies: a top-level Statement list. Version and
// Id are ignored.
type bucketPolicyDoc struct {
	Statement []bucketPolicyStatement `json:"Statement"`
}

// bucketPolicyStatement is the subset of an IAM statement we care about.
// Principal uses RawMessage so we can decode any of AWS's accepted shapes
// (string | object with "AWS" key whose value is string | []string).
// Condition is decoded as a structured map so we can JSON re-encode it
// for edge metadata.
type bucketPolicyStatement struct {
	Effect    string                                `json:"Effect"`
	Principal json.RawMessage                       `json:"Principal"`
	Action    json.RawMessage                       `json:"Action"`
	Resource  json.RawMessage                       `json:"Resource"`
	Condition map[string]map[string]json.RawMessage `json:"Condition,omitempty"`
}

// parseBucketPolicy unmarshals a bucket policy JSON document and flattens
// it into one EdgeGrants edge per principal per statement. Only "Allow"
// statements produce grants. Statements with no extractable principal are
// skipped.
func parseBucketPolicy(bucketARN, policyJSON string) ([]cloud.EdgeSpec, error) {
	if policyJSON == "" {
		return nil, nil
	}
	var doc bucketPolicyDoc
	if err := json.Unmarshal([]byte(policyJSON), &doc); err != nil {
		return nil, err
	}

	var edges []cloud.EdgeSpec
	for _, stmt := range doc.Statement {
		if stmt.Effect != "Allow" {
			continue
		}
		principals := extractPrincipals(stmt.Principal)
		if len(principals) == 0 {
			continue
		}

		var metadata map[string]string
		if len(stmt.Condition) > 0 {
			raw, err := json.Marshal(stmt.Condition)
			if err == nil {
				metadata = map[string]string{"condition": string(raw)}
			}
		}
		for _, p := range principals {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     bucketARN,
				TargetID:     p,
				Relationship: kgtypes.EdgeGrants,
				Metadata:     metadata,
			})
		}
	}
	return edges, nil
}

// extractPrincipals flattens an AWS policy Principal block into a list of
// principal strings. Accepted shapes:
//
//   - "*"                                               → ["*"]
//   - {"AWS": "arn"}                                    → ["arn"]
//   - {"AWS": ["arn1", "arn2"]}                         → ["arn1","arn2"]
//   - {"Service": "s3.amazonaws.com"}                   → ["s3.amazonaws.com"]
//   - {"Service": ["lambda","events"]}                  → ["lambda","events"]
//
// Empty or unparseable blocks return an empty slice.
func extractPrincipals(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}

	// Case 1: bare string (usually "*").
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		return []string{s}
	}

	// Case 2: object with AWS/Service/Federated/CanonicalUser keys.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	var out []string
	for _, v := range obj {
		out = append(out, decodeStringOrList(v)...)
	}
	return out
}

// decodeStringOrList decodes a JSON value that may be a single string or an
// array of strings into a []string. Other shapes yield an empty slice.
func decodeStringOrList(raw json.RawMessage) []string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	return nil
}

// isNoSuchBucketEncryption detects the "no SSE configured" error. AWS SDK
// v2 does not model this as a typed error for S3, so we match on the
// smithy APIError code.
func isNoSuchBucketEncryption(err error) bool {
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		return apiErr.ErrorCode() == "ServerSideEncryptionConfigurationNotFoundError"
	}
	return false
}
