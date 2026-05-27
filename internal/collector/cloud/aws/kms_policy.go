// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectKeyPolicyEdges calls GetKeyPolicy for the default policy and
// parses the returned IAM policy JSON into one EdgeGrants edge per
// (statement, principal) pair. Condition blocks are serialized as compact
// JSON in Metadata["condition"]. Errors fail-open — logging rather than
// aborting collection.
func (c *kmsCollector) collectKeyPolicyEdges(ctx context.Context, keyARN, keyID string) []cloud.EdgeSpec {
	out, err := c.client.GetKeyPolicy(ctx, &kms.GetKeyPolicyInput{
		KeyId:      awssdk.String(keyID),
		PolicyName: awssdk.String("default"),
	})
	if err != nil {
		slog.Warn("kms: get key policy", "key", keyID, "error", err)
		return nil
	}
	if out == nil || out.Policy == nil {
		return nil
	}

	edges, perr := parseKeyPolicy(keyARN, awssdk.ToString(out.Policy))
	if perr != nil {
		slog.Warn("kms: parse key policy", "key", keyID, "error", perr)
		return nil
	}
	return edges
}

// parseKeyPolicy unmarshals a KMS key policy JSON document (same IAM
// format as bucket policies) and flattens it into EdgeGrants edges. Only
// "Allow" statements produce edges.
func parseKeyPolicy(keyARN, policyJSON string) ([]cloud.EdgeSpec, error) {
	if policyJSON == "" {
		return nil, nil
	}
	// Reuse the same policy parsing types from s3_policy.go — the KMS key
	// policy JSON format is identical to S3 bucket policy.
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
				SourceID:     keyARN,
				TargetID:     p,
				Relationship: kgtypes.EdgeGrants,
				Metadata:     metadata,
			})
		}
	}
	return edges, nil
}

// collectAliasEdges calls ListAliases for this specific key and emits
// EdgeContains from the key ARN to each alias ARN. Each alias is a child
// resource of the key it points at.
func (c *kmsCollector) collectAliasEdges(ctx context.Context, keyARN, keyID string) []cloud.EdgeSpec {
	out, err := c.client.ListAliases(ctx, &kms.ListAliasesInput{
		KeyId: awssdk.String(keyID),
	})
	if err != nil {
		slog.Warn("kms: list aliases", "key", keyID, "error", err)
		return nil
	}
	if out == nil {
		return nil
	}

	var edges []cloud.EdgeSpec
	for _, alias := range out.Aliases {
		aliasARN := awssdk.ToString(alias.AliasArn)
		if aliasARN == "" {
			aliasARN = fmt.Sprintf("arn:aws:kms:%s:%s:alias/%s",
				c.region, c.accountID, awssdk.ToString(alias.AliasName))
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     keyARN,
			TargetID:     aliasARN,
			Relationship: kgtypes.EdgeContains,
		})
	}
	return edges
}
