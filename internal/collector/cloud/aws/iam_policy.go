// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// managedPolicyContent is the curated wire shape for AWS managed IAM policies.
// Stored in iam-policy node Content as a JSON envelope. Curated projection of
// iamtypes.Policy (collector-owned, decoupled from SDK version). Bundles
// policy metadata with the URL-encoded default-version document body so the
// Phase 1 parser can extract both from a single field.
//
// Mirrors the acmCertificateContent exemplar at cmd/knowledge/internal/collector/cloud/aws/acm.go:201.
// Time fields are encoded as RFC3339 strings (same convention as
// acmCertificateContent.NotBefore / NotAfter — see acm.go:100-105 for the
// nil-guarded .Format(time.RFC3339) pattern).
//
// Note: AttachmentCount uses int32 (not *int32) — nil and zero are
// intentionally indistinguishable on the wire because no current reader
// consumes attachment_count from Content. iamPolicyMetadata (below, line 96)
// preserves the nil/zero distinction for callers that need it via node
// Metadata: it nil-checks p.AttachmentCount before emitting the
// "attachment_count" entry. The wire struct deliberately diverges from
// that for simplicity — readers wanting the distinction read Metadata.
type managedPolicyContent struct {
	Arn              string `json:"arn"`
	PolicyName       string `json:"policy_name"`
	DefaultVersionId string `json:"default_version_id,omitempty"`
	AttachmentCount  int32  `json:"attachment_count,omitempty"`
	IsAttachable     bool   `json:"is_attachable,omitempty"`
	Path             string `json:"path,omitempty"`
	CreateDate       string `json:"create_date,omitempty"`
	UpdateDate       string `json:"update_date,omitempty"`
	Document         string `json:"document,omitempty"`
}

// collectPolicies enumerates customer-managed policies (Scope: Local) and
// fetches the default-version document body for each. The policy metadata
// and the document live together inside Content as a managedPolicyContent
// JSON envelope.
func (c *iamCollector) collectPolicies(ctx context.Context) ([]cloud.ResourceSpec, error) {
	var resources []cloud.ResourceSpec

	paginator := iam.NewListPoliciesPaginator(c.client, &iam.ListPoliciesInput{
		Scope: iamtypes.PolicyScopeTypeLocal,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("iam: list policies: %w", err)
		}

		for _, policy := range page.Policies {
			document, err := c.fetchPolicyDocument(ctx, policy)
			if err != nil {
				return nil, err
			}

			envelope := managedPolicyContent{
				Arn:              awssdk.ToString(policy.Arn),
				PolicyName:       awssdk.ToString(policy.PolicyName),
				DefaultVersionId: awssdk.ToString(policy.DefaultVersionId),
				AttachmentCount:  awssdk.ToInt32(policy.AttachmentCount),
				IsAttachable:     policy.IsAttachable,
				Path:             awssdk.ToString(policy.Path),
				Document:         document,
			}
			if policy.CreateDate != nil {
				envelope.CreateDate = policy.CreateDate.Format(time.RFC3339)
			}
			if policy.UpdateDate != nil {
				envelope.UpdateDate = policy.UpdateDate.Format(time.RFC3339)
			}
			content, err := json.Marshal(envelope)
			if err != nil {
				return nil, fmt.Errorf("iam: marshal policy: %w", err)
			}

			resources = append(resources, cloud.ResourceSpec{
				ID:           awssdk.ToString(policy.Arn),
				Name:         awssdk.ToString(policy.PolicyName),
				ResourceType: "iam-policy",
				Content:      content,
				Metadata:     iamPolicyMetadata(policy),
			})
		}
	}

	return resources, nil
}

// fetchPolicyDocument calls GetPolicyVersion for the default version of a
// managed policy and returns the URL-encoded JSON document body. Returns an
// empty string if the policy has no default version (defensive — required
// field per IAM API contract but treated as soft-fail to keep collection
// resilient).
func (c *iamCollector) fetchPolicyDocument(ctx context.Context, policy iamtypes.Policy) (string, error) {
	if policy.DefaultVersionId == nil || *policy.DefaultVersionId == "" {
		return "", nil
	}

	out, err := c.client.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{
		PolicyArn: policy.Arn,
		VersionId: policy.DefaultVersionId,
	})
	if err != nil {
		return "", fmt.Errorf("iam: get policy version %s for %s: %w",
			awssdk.ToString(policy.DefaultVersionId),
			awssdk.ToString(policy.Arn),
			err)
	}
	if out.PolicyVersion == nil {
		return "", nil
	}
	return awssdk.ToString(out.PolicyVersion.Document), nil
}

// iamPolicyMetadata extracts discriminating fields from a managed policy.
func iamPolicyMetadata(p iamtypes.Policy) map[string]string {
	m := make(map[string]string, 3)
	if p.AttachmentCount != nil {
		m["attachment_count"] = fmt.Sprintf("%d", awssdk.ToInt32(p.AttachmentCount))
	}
	if p.IsAttachable {
		m["is_attachable"] = "true"
	}
	if path := awssdk.ToString(p.Path); path != "" && path != "/" {
		m["path"] = path
	}
	return m
}
