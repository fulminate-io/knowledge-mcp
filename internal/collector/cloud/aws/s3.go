// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// Canonical S3 ACL group URIs. A Grant whose Grantee URI matches either of
// these constants makes the bucket publicly readable/writable.
const (
	s3AllUsersGroupURI           = "http://acs.amazonaws.com/groups/global/AllUsers"
	s3AuthenticatedUsersGroupURI = "http://acs.amazonaws.com/groups/global/AuthenticatedUsers"
)

// s3API is the subset of the S3 client surface used by s3Collector. Defining
// it as an interface lets tests mock the S3 API without AWS credentials. The
// concrete *s3.Client satisfies this interface.
type s3API interface {
	ListBuckets(ctx context.Context, params *s3.ListBucketsInput, optFns ...func(*s3.Options)) (*s3.ListBucketsOutput, error)
	GetPublicAccessBlock(ctx context.Context, params *s3.GetPublicAccessBlockInput, optFns ...func(*s3.Options)) (*s3.GetPublicAccessBlockOutput, error)
	GetBucketPolicyStatus(ctx context.Context, params *s3.GetBucketPolicyStatusInput, optFns ...func(*s3.Options)) (*s3.GetBucketPolicyStatusOutput, error)
	GetBucketAcl(ctx context.Context, params *s3.GetBucketAclInput, optFns ...func(*s3.Options)) (*s3.GetBucketAclOutput, error)
	GetBucketNotificationConfiguration(ctx context.Context, params *s3.GetBucketNotificationConfigurationInput, optFns ...func(*s3.Options)) (*s3.GetBucketNotificationConfigurationOutput, error)
	GetBucketEncryption(ctx context.Context, params *s3.GetBucketEncryptionInput, optFns ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error)
	GetBucketPolicy(ctx context.Context, params *s3.GetBucketPolicyInput, optFns ...func(*s3.Options)) (*s3.GetBucketPolicyOutput, error)
	GetBucketReplication(ctx context.Context, params *s3.GetBucketReplicationInput, optFns ...func(*s3.Options)) (*s3.GetBucketReplicationOutput, error)
}

type s3Collector struct {
	client    s3API
	region    string
	accountID string
}

func newS3Collector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &s3Collector{
		client:    s3.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *s3Collector) Name() string { return "s3" }

// s3BucketContent is the envelope marshaled into node.Content for each S3
// bucket. It embeds the raw Bucket struct plus public-exposure fields
// populated via separate GetPublicAccessBlock / GetBucketPolicyStatus /
// GetBucketAcl calls. Consumed by the topology/public_exposure analyzer.
type s3BucketContent struct {
	Bucket                   s3types.Bucket      `json:"bucket"`
	PublicAccessBlock        *publicAccessBlock  `json:"public_access_block,omitempty"`
	PublicAccessBlockMissing bool                `json:"public_access_block_missing,omitempty"`
	BucketPolicyStatus       *bucketPolicyStatus `json:"bucket_policy_status,omitempty"`
	ACLPublicGrants          []aclPublicGrant    `json:"acl_public_grants,omitempty"`
	NotificationTargets      []string            `json:"notification_targets,omitempty"`
}

// publicAccessBlock is the four-flag struct returned by GetPublicAccessBlock.
// nil pointer on any of the source fields is treated as false (AWS default
// for unset flags in older buckets).
type publicAccessBlock struct {
	BlockPublicAcls       bool `json:"block_public_acls"`
	BlockPublicPolicy     bool `json:"block_public_policy"`
	IgnorePublicAcls      bool `json:"ignore_public_acls"`
	RestrictPublicBuckets bool `json:"restrict_public_buckets"`
}

type bucketPolicyStatus struct {
	IsPublic bool `json:"is_public"`
}

// aclPublicGrant records one ACL grant pointing at a public group URI.
type aclPublicGrant struct {
	GroupURI   string `json:"group_uri"`
	Permission string `json:"permission"`
}

func (c *s3Collector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	// S3 ListBuckets returns all buckets (no pagination needed for the list call).
	out, err := c.client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("s3: list buckets: %w", err)
	}

	for _, bucket := range out.Buckets {
		bucketName := awssdk.ToString(bucket.Name)
		envelope := s3BucketContent{Bucket: bucket}

		c.enrichPublicAccessBlock(ctx, bucketName, &envelope)
		c.enrichBucketPolicyStatus(ctx, bucketName, &envelope)
		c.enrichBucketACL(ctx, bucketName, &envelope)
		c.enrichNotificationTargets(ctx, bucketName, &envelope)

		content, err := json.Marshal(envelope)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("s3: marshal: %w", err)
		}

		// S3 ARNs use arn:aws:s3:::bucket-name format (no region, no account).
		bucketARN := fmt.Sprintf("arn:aws:s3:::%s", bucketName)

		resources = append(resources, cloud.ResourceSpec{
			ID:           bucketARN,
			Name:         bucketName,
			ResourceType: "s3-bucket",
			Region:       c.region,
			Content:      content,
			Metadata:     s3BucketMetadata(bucket),
		})

		for _, targetARN := range envelope.NotificationTargets {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     bucketARN,
				TargetID:     targetARN,
				Relationship: kgtypes.EdgeTriggers,
			})
		}

		edges = append(edges, c.collectEncryptionEdges(ctx, bucketARN, bucketName)...)
		edges = append(edges, c.collectPolicyGrantEdges(ctx, bucketARN, bucketName)...)
		edges = append(edges, c.collectReplicationEdges(ctx, bucketARN, bucketName)...)
	}

	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

// enrichPublicAccessBlock calls GetPublicAccessBlock and populates the
// envelope. Missing configuration (NoSuchPublicAccessBlockConfiguration) is
// recorded as PublicAccessBlockMissing=true, not as an error. Other errors
// are logged fail-open.
func (c *s3Collector) enrichPublicAccessBlock(ctx context.Context, bucketName string, env *s3BucketContent) {
	out, err := c.client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{
		Bucket: awssdk.String(bucketName),
	})
	if err != nil {
		if isNoSuchPublicAccessBlock(err) {
			env.PublicAccessBlockMissing = true
			return
		}
		slog.Warn("s3: get public access block", "bucket", bucketName, "error", err)
		return
	}
	if out == nil || out.PublicAccessBlockConfiguration == nil {
		env.PublicAccessBlockMissing = true
		return
	}
	cfg := out.PublicAccessBlockConfiguration
	env.PublicAccessBlock = &publicAccessBlock{
		BlockPublicAcls:       awssdk.ToBool(cfg.BlockPublicAcls),
		BlockPublicPolicy:     awssdk.ToBool(cfg.BlockPublicPolicy),
		IgnorePublicAcls:      awssdk.ToBool(cfg.IgnorePublicAcls),
		RestrictPublicBuckets: awssdk.ToBool(cfg.RestrictPublicBuckets),
	}
}

// enrichBucketPolicyStatus calls GetBucketPolicyStatus. NoSuchBucketPolicy
// is expected for most buckets and is silently ignored (field stays nil =
// no public policy). Other errors fail-open.
func (c *s3Collector) enrichBucketPolicyStatus(ctx context.Context, bucketName string, env *s3BucketContent) {
	out, err := c.client.GetBucketPolicyStatus(ctx, &s3.GetBucketPolicyStatusInput{
		Bucket: awssdk.String(bucketName),
	})
	if err != nil {
		if isNoSuchBucketPolicy(err) {
			return
		}
		slog.Warn("s3: get bucket policy status", "bucket", bucketName, "error", err)
		return
	}
	if out == nil || out.PolicyStatus == nil {
		return
	}
	env.BucketPolicyStatus = &bucketPolicyStatus{
		IsPublic: awssdk.ToBool(out.PolicyStatus.IsPublic),
	}
}

// enrichBucketACL calls GetBucketAcl and records any grants targeting the
// AllUsers or AuthenticatedUsers public group URIs. Private-only ACLs leave
// the slice empty. Errors fail-open.
func (c *s3Collector) enrichBucketACL(ctx context.Context, bucketName string, env *s3BucketContent) {
	out, err := c.client.GetBucketAcl(ctx, &s3.GetBucketAclInput{
		Bucket: awssdk.String(bucketName),
	})
	if err != nil {
		slog.Warn("s3: get bucket acl", "bucket", bucketName, "error", err)
		return
	}
	if out == nil {
		return
	}
	for _, grant := range out.Grants {
		if grant.Grantee == nil || grant.Grantee.URI == nil {
			continue
		}
		uri := awssdk.ToString(grant.Grantee.URI)
		if uri != s3AllUsersGroupURI && uri != s3AuthenticatedUsersGroupURI {
			continue
		}
		env.ACLPublicGrants = append(env.ACLPublicGrants, aclPublicGrant{
			GroupURI:   uri,
			Permission: string(grant.Permission),
		})
	}
}

// enrichNotificationTargets calls GetBucketNotificationConfiguration and
// collects Lambda, SQS, and SNS notification target ARNs. Errors fail-open.
func (c *s3Collector) enrichNotificationTargets(ctx context.Context, bucketName string, env *s3BucketContent) {
	out, err := c.client.GetBucketNotificationConfiguration(ctx, &s3.GetBucketNotificationConfigurationInput{
		Bucket: awssdk.String(bucketName),
	})
	if err != nil {
		slog.Warn("s3: get notification configuration", "bucket", bucketName, "error", err)
		return
	}
	if out == nil {
		return
	}
	for _, cfg := range out.LambdaFunctionConfigurations {
		if cfg.LambdaFunctionArn != nil {
			env.NotificationTargets = append(env.NotificationTargets, awssdk.ToString(cfg.LambdaFunctionArn))
		}
	}
	for _, cfg := range out.QueueConfigurations {
		if cfg.QueueArn != nil {
			env.NotificationTargets = append(env.NotificationTargets, awssdk.ToString(cfg.QueueArn))
		}
	}
	for _, cfg := range out.TopicConfigurations {
		if cfg.TopicArn != nil {
			env.NotificationTargets = append(env.NotificationTargets, awssdk.ToString(cfg.TopicArn))
		}
	}
}

// collectReplicationEdges calls GetBucketReplication and emits one
// EdgeReplicatesTo per rule destination. ReplicationConfigurationNotFoundError
// (the "no replication configured" sentinel) is silently ignored so unrelated
// errors don't get swallowed. Other errors fail-open (warn + skip).
func (c *s3Collector) collectReplicationEdges(ctx context.Context, bucketARN, bucketName string) []cloud.EdgeSpec {
	out, err := c.client.GetBucketReplication(ctx, &s3.GetBucketReplicationInput{
		Bucket: awssdk.String(bucketName),
	})
	if err != nil {
		if isNoSuchBucketReplication(err) {
			return nil
		}
		slog.Warn("s3: get bucket replication", "bucket", bucketName, "error", err)
		return nil
	}
	if out == nil || out.ReplicationConfiguration == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, rule := range out.ReplicationConfiguration.Rules {
		if rule.Destination == nil || rule.Destination.Bucket == nil {
			continue
		}
		dest := awssdk.ToString(rule.Destination.Bucket)
		if dest == "" {
			continue
		}
		md := map[string]string{}
		if rule.ID != nil {
			md["rule_id"] = awssdk.ToString(rule.ID)
		}
		if rule.Status != "" {
			md["rule_status"] = string(rule.Status)
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     bucketARN,
			TargetID:     dest,
			Relationship: kgtypes.EdgeReplicatesTo,
			Metadata:     md,
		})
	}
	return edges
}

// isNoSuchBucketReplication detects the "no replication" sentinel. The S3 SDK
// returns ReplicationConfigurationNotFoundError as a smithy APIError code.
func isNoSuchBucketReplication(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "ReplicationConfigurationNotFoundError"
	}
	return false
}

// isNoSuchPublicAccessBlock detects the "no PAB configured" error. S3 does
// not expose this as a typed error in aws-sdk-go-v2, so we match on the
// smithy APIError code.
func isNoSuchPublicAccessBlock(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "NoSuchPublicAccessBlockConfiguration"
	}
	return false
}

// isNoSuchBucketPolicy detects the "no policy attached" error. Like PAB this
// is a string-coded smithy error.
func isNoSuchBucketPolicy(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "NoSuchBucketPolicy"
	}
	return false
}

// s3BucketMetadata extracts discriminating fields from an S3 bucket. CreationDate
// is omitted when nil. S3 buckets have minimal API-level discriminators; richer
// posture (encryption, public access) is captured in edges + content envelope.
func s3BucketMetadata(b s3types.Bucket) map[string]string {
	m := make(map[string]string, 1)
	if b.CreationDate != nil {
		m["creation_date"] = b.CreationDate.UTC().Format("2006-01-02T15:04:05Z")
	}
	return m
}
