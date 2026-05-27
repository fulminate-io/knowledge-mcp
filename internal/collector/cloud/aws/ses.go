// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// sesv2API is the subset of the SESv2 client used for identity enumeration
// and DKIM details. The concrete *sesv2.Client satisfies this interface.
type sesv2API interface {
	ListEmailIdentities(ctx context.Context, params *sesv2.ListEmailIdentitiesInput, optFns ...func(*sesv2.Options)) (*sesv2.ListEmailIdentitiesOutput, error)
	GetEmailIdentity(ctx context.Context, params *sesv2.GetEmailIdentityInput, optFns ...func(*sesv2.Options)) (*sesv2.GetEmailIdentityOutput, error)
}

// sesv1API is the subset of the v1 SES client used for SNS notification
// attributes and receipt rules. SESv2 does not expose these APIs.
type sesv1API interface {
	GetIdentityNotificationAttributes(ctx context.Context, params *ses.GetIdentityNotificationAttributesInput, optFns ...func(*ses.Options)) (*ses.GetIdentityNotificationAttributesOutput, error)
	DescribeActiveReceiptRuleSet(ctx context.Context, params *ses.DescribeActiveReceiptRuleSetInput, optFns ...func(*ses.Options)) (*ses.DescribeActiveReceiptRuleSetOutput, error)
}

type sesCollector struct {
	v2client  sesv2API
	v1client  sesv1API
	region    string
	accountID string
}

func newSESCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &sesCollector{
		v2client:  sesv2.NewFromConfig(cfg),
		v1client:  ses.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *sesCollector) Name() string { return "ses" }

func (c *sesCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	// Collect identities via sesv2 (DKIM details).
	identityNames, err := c.collectIdentities(ctx, &resources)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}

	// Collect SNS notification topics via v1 API.
	edges = append(edges, c.collectNotificationEdges(ctx, identityNames)...)

	// Collect receipt rules (account-scoped, once per region).
	ruleResources, ruleEdges := c.collectReceiptRules(ctx)
	resources = append(resources, ruleResources...)
	edges = append(edges, ruleEdges...)

	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

// collectIdentities enumerates all SES email identities using sesv2,
// fetches DKIM details, and appends resources. Returns identity names
// for the v1 notification attribute lookup.
func (c *sesCollector) collectIdentities(
	ctx context.Context,
	resources *[]cloud.ResourceSpec,
) ([]string, error) {
	var names []string
	var nextToken *string
	for {
		page, err := c.v2client.ListEmailIdentities(ctx, &sesv2.ListEmailIdentitiesInput{
			NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("ses: list email identities: %w", err)
		}
		for _, identity := range page.EmailIdentities {
			name := awssdk.ToString(identity.IdentityName)
			names = append(names, name)

			res, err := c.collectIdentity(ctx, name)
			if err != nil {
				return nil, err
			}
			*resources = append(*resources, res)
		}
		nextToken = page.NextToken
		if nextToken == nil {
			break
		}
	}
	return names, nil
}

// collectIdentity fetches details for a single SES email identity.
func (c *sesCollector) collectIdentity(ctx context.Context, identityName string) (cloud.ResourceSpec, error) {
	out, err := c.v2client.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{
		EmailIdentity: awssdk.String(identityName),
	})
	if err != nil {
		return cloud.ResourceSpec{}, fmt.Errorf("ses: get email identity %s: %w", identityName, err)
	}

	content, err := json.Marshal(out)
	if err != nil {
		return cloud.ResourceSpec{}, fmt.Errorf("ses: marshal: %w", err)
	}

	identityARN := fmt.Sprintf("arn:aws:ses:%s:%s:identity/%s", c.region, c.accountID, identityName)

	return cloud.ResourceSpec{
		ID:           identityARN,
		Name:         identityName,
		ResourceType: "ses-identity",
		Region:       c.region,
		Content:      content,
		Metadata:     sesIdentityMetadata(out),
	}, nil
}

// sesIdentityMetadata extracts discriminating fields from an SES identity.
func sesIdentityMetadata(out *sesv2.GetEmailIdentityOutput) map[string]string {
	m := make(map[string]string, 2)
	if t := string(out.IdentityType); t != "" {
		m["identity_type"] = t
	}
	if out.VerifiedForSendingStatus {
		m["verified_for_sending"] = "true"
	}
	return m
}
