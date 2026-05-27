// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// kmsAPI is the subset of the KMS client surface used by kmsCollector.
// Defining it as an interface lets tests mock KMS without AWS credentials.
type kmsAPI interface {
	ListKeys(ctx context.Context, params *kms.ListKeysInput, optFns ...func(*kms.Options)) (*kms.ListKeysOutput, error)
	DescribeKey(ctx context.Context, params *kms.DescribeKeyInput, optFns ...func(*kms.Options)) (*kms.DescribeKeyOutput, error)
	GetKeyPolicy(ctx context.Context, params *kms.GetKeyPolicyInput, optFns ...func(*kms.Options)) (*kms.GetKeyPolicyOutput, error)
	ListAliases(ctx context.Context, params *kms.ListAliasesInput, optFns ...func(*kms.Options)) (*kms.ListAliasesOutput, error)
}

type kmsCollector struct {
	client    kmsAPI
	region    string
	accountID string
}

func newKMSCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &kmsCollector{
		client:    kms.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *kmsCollector) Name() string { return "kms" }

func (c *kmsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	var nextToken *string
	for {
		page, err := c.client.ListKeys(ctx, &kms.ListKeysInput{Marker: nextToken})
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("kms: list keys: %w", err)
		}

		for _, key := range page.Keys {
			keyID := awssdk.ToString(key.KeyId)
			res, keyEdges, err := c.collectKey(ctx, keyID)
			if err != nil {
				return cloud.SubCollectorResult{}, err
			}
			resources = append(resources, res)
			edges = append(edges, keyEdges...)
		}

		if !page.Truncated {
			break
		}
		nextToken = page.NextMarker
	}

	return cloud.SubCollectorResult{Resources: resources, Edges: edges}, nil
}

// collectKey fetches key metadata and builds a resource plus key-policy
// grant edges and alias edges.
// All keys are collected including AWS-managed — KeyManager is included in
// metadata so queries can distinguish between AWS and CUSTOMER managed keys.
func (c *kmsCollector) collectKey(ctx context.Context, keyID string) (cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	desc, err := c.client.DescribeKey(ctx, &kms.DescribeKeyInput{
		KeyId: awssdk.String(keyID),
	})
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("kms: describe key %s: %w", keyID, err)
	}

	meta := desc.KeyMetadata
	content, err := json.Marshal(meta)
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("kms: marshal: %w", err)
	}

	keyARN := awssdk.ToString(meta.Arn)

	name := awssdk.ToString(meta.Description)
	if name == "" {
		name = awssdk.ToString(meta.KeyId)
	}

	edges := c.collectKeyPolicyEdges(ctx, keyARN, keyID)
	edges = append(edges, c.collectAliasEdges(ctx, keyARN, keyID)...)

	return cloud.ResourceSpec{
		ID:           keyARN,
		Name:         name,
		ResourceType: "kms-key",
		Region:       c.region,
		Content:      content,
		Metadata:     kmsKeyMetadata(meta),
	}, edges, nil
}

// kmsKeyMetadata extracts discriminating fields from a KMS key. Includes the
// existing KeyManager field plus key_state, key_usage, and key_spec.
func kmsKeyMetadata(meta *kmstypes.KeyMetadata) map[string]string {
	if meta == nil {
		return nil
	}
	m := make(map[string]string, 4)
	if k := string(meta.KeyManager); k != "" {
		m["KeyManager"] = k
	}
	if s := string(meta.KeyState); s != "" {
		m["key_state"] = s
	}
	if u := string(meta.KeyUsage); u != "" {
		m["key_usage"] = u
	}
	if s := string(meta.KeySpec); s != "" {
		m["key_spec"] = s
	}
	return m
}
