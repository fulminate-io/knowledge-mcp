// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type secretsManagerCollector struct {
	client    *secretsmanager.Client
	region    string
	accountID string
}

// newSecretsManagerCollector creates a Secrets Manager subcollector.
// Only secret metadata is collected — secret values are NEVER retrieved.
func newSecretsManagerCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &secretsManagerCollector{
		client:    secretsmanager.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *secretsManagerCollector) Name() string { return "secretsmanager" }

func (c *secretsManagerCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := secretsmanager.NewListSecretsPaginator(c.client, &secretsmanager.ListSecretsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("secretsmanager: list secrets: %w", err)
		}

		for _, secret := range page.SecretList {
			// Marshal metadata only — SecretListEntry never contains secret values.
			content, err := json.Marshal(secret)
			if err != nil {
				return cloud.SubCollectorResult{}, fmt.Errorf("secretsmanager: marshal: %w", err)
			}

			secretARN := awssdk.ToString(secret.ARN)
			secretName := awssdk.ToString(secret.Name)

			resources = append(resources, cloud.ResourceSpec{
				ID:           secretARN,
				Name:         secretName,
				ResourceType: "secretsmanager-secret",
				Region:       c.region,
				Content:      content,
				Metadata:     secretsManagerSecretMetadata(secret),
			})

			// Secret → KMS key (server-side encryption)
			if kmsKeyID := awssdk.ToString(secret.KmsKeyId); kmsKeyID != "" {
				kmsARN := resolveKMSKeyARN(kmsKeyID, c.region, c.accountID)
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     secretARN,
					TargetID:     kmsARN,
					Relationship: kgtypes.EdgeEncryptsWith,
					Metadata:     map[string]string{"encryption_scope": "secret"},
				})
			}
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// secretsManagerSecretMetadata extracts discriminating fields from a secret entry.
func secretsManagerSecretMetadata(s smtypes.SecretListEntry) map[string]string {
	m := make(map[string]string, 2)
	if d := awssdk.ToString(s.Description); d != "" {
		m["description"] = d
	}
	if k := awssdk.ToString(s.KmsKeyId); k != "" {
		m["kms_key_id"] = k
	}
	return m
}
