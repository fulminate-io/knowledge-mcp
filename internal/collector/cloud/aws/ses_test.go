// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	sestypes "github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sesv2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestSESCollector_Name(t *testing.T) {
	c := &sesCollector{}
	assert.Equal(t, "ses", c.Name())
}

// --- Fake clients ---

type fakeSESv2API struct {
	identities []sesv2types.IdentityInfo
	details    map[string]*sesv2.GetEmailIdentityOutput
}

func (f *fakeSESv2API) ListEmailIdentities(_ context.Context, _ *sesv2.ListEmailIdentitiesInput, _ ...func(*sesv2.Options)) (*sesv2.ListEmailIdentitiesOutput, error) {
	return &sesv2.ListEmailIdentitiesOutput{EmailIdentities: f.identities}, nil
}

func (f *fakeSESv2API) GetEmailIdentity(_ context.Context, in *sesv2.GetEmailIdentityInput, _ ...func(*sesv2.Options)) (*sesv2.GetEmailIdentityOutput, error) {
	name := awssdk.ToString(in.EmailIdentity)
	if d, ok := f.details[name]; ok {
		return d, nil
	}
	return &sesv2.GetEmailIdentityOutput{}, nil
}

type fakeSESv1API struct {
	notificationAttrs map[string]sestypes.IdentityNotificationAttributes
	activeRuleSet     *ses.DescribeActiveReceiptRuleSetOutput
}

func (f *fakeSESv1API) GetIdentityNotificationAttributes(_ context.Context, _ *ses.GetIdentityNotificationAttributesInput, _ ...func(*ses.Options)) (*ses.GetIdentityNotificationAttributesOutput, error) {
	return &ses.GetIdentityNotificationAttributesOutput{
		NotificationAttributes: f.notificationAttrs,
	}, nil
}

func (f *fakeSESv1API) DescribeActiveReceiptRuleSet(_ context.Context, _ *ses.DescribeActiveReceiptRuleSetInput, _ ...func(*ses.Options)) (*ses.DescribeActiveReceiptRuleSetOutput, error) {
	if f.activeRuleSet != nil {
		return f.activeRuleSet, nil
	}
	return &ses.DescribeActiveReceiptRuleSetOutput{}, nil
}

// TestSESCollector_NotificationEdges verifies EdgeNotifiesVia edges from
// identity → SNS topic for bounce/complaint/delivery notifications.
func TestSESCollector_NotificationEdges(t *testing.T) {
	bounceTopic := "arn:aws:sns:us-east-1:111111111111:ses-bounces"
	complaintTopic := "arn:aws:sns:us-east-1:111111111111:ses-complaints"
	identityName := "example.com"

	v2 := &fakeSESv2API{
		identities: []sesv2types.IdentityInfo{
			{IdentityName: awssdk.String(identityName)},
		},
		details: map[string]*sesv2.GetEmailIdentityOutput{
			identityName: {},
		},
	}
	v1 := &fakeSESv1API{
		notificationAttrs: map[string]sestypes.IdentityNotificationAttributes{
			identityName: {
				BounceTopic:    awssdk.String(bounceTopic),
				ComplaintTopic: awssdk.String(complaintTopic),
				// DeliveryTopic intentionally nil — should be skipped.
			},
		},
	}

	c := &sesCollector{v2client: v2, v1client: v1, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	var notifyTargets []string
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeNotifiesVia {
			notifyTargets = append(notifyTargets, e.TargetID)
		}
	}
	assert.ElementsMatch(t, []string{bounceTopic, complaintTopic}, notifyTargets)
}

// TestSESCollector_ReceiptRuleLambda verifies receipt rule Lambda action
// emits EdgeTriggers.
func TestSESCollector_ReceiptRuleLambda(t *testing.T) {
	lambdaARN := "arn:aws:lambda:us-east-1:111111111111:function:process-email"
	v2 := &fakeSESv2API{}
	v1 := &fakeSESv1API{
		activeRuleSet: &ses.DescribeActiveReceiptRuleSetOutput{
			Metadata: &sestypes.ReceiptRuleSetMetadata{Name: awssdk.String("default-rule-set")},
			Rules: []sestypes.ReceiptRule{{
				Name: awssdk.String("invoke-lambda"),
				Actions: []sestypes.ReceiptAction{{
					LambdaAction: &sestypes.LambdaAction{
						FunctionArn: awssdk.String(lambdaARN),
					},
				}},
			}},
		},
	}

	c := &sesCollector{v2client: v2, v1client: v1, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	// One receipt rule resource.
	require.Len(t, result.Resources, 1)
	assert.Equal(t, "ses-receipt-rule", result.Resources[0].ResourceType)

	var found bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeTriggers && e.TargetID == lambdaARN {
			found = true
		}
	}
	assert.True(t, found, "expected EdgeTriggers from receipt rule → Lambda")
}

// TestSESCollector_ReceiptRuleS3WithKMS verifies receipt rule S3 action
// emits EdgeTriggers to the bucket and EdgeEncryptsWith to the KMS key.
func TestSESCollector_ReceiptRuleS3WithKMS(t *testing.T) {
	kmsARN := "arn:aws:kms:us-east-1:111111111111:key/ses-s3-key"
	v2 := &fakeSESv2API{}
	v1 := &fakeSESv1API{
		activeRuleSet: &ses.DescribeActiveReceiptRuleSetOutput{
			Metadata: &sestypes.ReceiptRuleSetMetadata{Name: awssdk.String("production")},
			Rules: []sestypes.ReceiptRule{{
				Name: awssdk.String("archive"),
				Actions: []sestypes.ReceiptAction{{
					S3Action: &sestypes.S3Action{
						BucketName: awssdk.String("email-archive"),
						KmsKeyArn:  awssdk.String(kmsARN),
					},
				}},
			}},
		},
	}

	c := &sesCollector{v2client: v2, v1client: v1, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var hasTriggers, hasEncrypts bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeTriggers && e.TargetID == "arn:aws:s3:::email-archive" {
			hasTriggers = true
		}
		if e.Relationship == kgtypes.EdgeEncryptsWith && e.TargetID == kmsARN {
			hasEncrypts = true
		}
	}
	assert.True(t, hasTriggers, "expected EdgeTriggers to S3 bucket")
	assert.True(t, hasEncrypts, "expected EdgeEncryptsWith to KMS key")
}

// TestSESCollector_ReceiptRuleSNS verifies receipt rule SNS action
// emits EdgeNotifiesVia.
func TestSESCollector_ReceiptRuleSNS(t *testing.T) {
	snsARN := "arn:aws:sns:us-east-1:111111111111:email-notify"
	v2 := &fakeSESv2API{}
	v1 := &fakeSESv1API{
		activeRuleSet: &ses.DescribeActiveReceiptRuleSetOutput{
			Metadata: &sestypes.ReceiptRuleSetMetadata{Name: awssdk.String("my-rules")},
			Rules: []sestypes.ReceiptRule{{
				Name: awssdk.String("notify"),
				Actions: []sestypes.ReceiptAction{{
					SNSAction: &sestypes.SNSAction{
						TopicArn: awssdk.String(snsARN),
					},
				}},
			}},
		},
	}

	c := &sesCollector{v2client: v2, v1client: v1, region: "us-east-1", accountID: "111111111111"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var found bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeNotifiesVia && e.TargetID == snsARN {
			found = true
		}
	}
	assert.True(t, found, "expected EdgeNotifiesVia from receipt rule → SNS")
}
