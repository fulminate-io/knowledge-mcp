// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// secretsSubCollector collects Secret Manager secrets.
// CRITICAL: Only metadata is collected, NEVER secret values.
type secretsSubCollector struct {
	client    *secretmanager.Client
	projectID string
}

func newSecretsSubCollector(client *secretmanager.Client, projectID string) *secretsSubCollector {
	return &secretsSubCollector{client: client, projectID: projectID}
}

func (c *secretsSubCollector) Name() string { return "gcp-secrets" }

func (c *secretsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.ListSecrets(ctx, &secretmanagerpb.ListSecretsRequest{
		Parent: "projects/" + c.projectID,
	})

	for {
		secret, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		name := secret.GetName()
		if name == "" {
			continue
		}

		// Marshal only metadata fields — never secret values.
		metadata := map[string]any{
			"name":        name,
			"labels":      secret.GetLabels(),
			"replication": secret.GetReplication(),
		}
		content, err := json.Marshal(metadata)
		if err != nil {
			continue
		}

		spec := cloud.ResourceSpec{
			ID:           name,
			Name:         extractLast(name),
			ResourceType: "gcp:secretmanager:secret",
			Content:      content,
		}

		// Copy labels into node metadata for search.
		labels := secret.GetLabels()
		if len(labels) > 0 {
			spec.Metadata = make(map[string]string, len(labels))
			for k, v := range labels {
				spec.Metadata["label:"+k] = v
			}
		}

		result.Resources = append(result.Resources, spec)

		// ENCRYPTS_WITH edges when CMEK is configured on replication.
		result.Edges = append(result.Edges, secretCMEKEdges(name, secret)...)
	}

	return result, nil
}

// secretCMEKEdges extracts ENCRYPTS_WITH edges from a secret's replication config.
func secretCMEKEdges(secretName string, secret *secretmanagerpb.Secret) []cloud.EdgeSpec {
	repl := secret.GetReplication()
	if repl == nil {
		return nil
	}

	var edges []cloud.EdgeSpec

	// Automatic replication with CMEK.
	if auto := repl.GetAutomatic(); auto != nil {
		if cme := auto.GetCustomerManagedEncryption(); cme != nil {
			if key := cme.GetKmsKeyName(); key != "" {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     secretName,
					TargetID:     key,
					Relationship: kgtypes.EdgeEncryptsWith,
				})
			}
		}
	}

	// User-managed replication with per-replica CMEK.
	if um := repl.GetUserManaged(); um != nil {
		for _, replica := range um.GetReplicas() {
			if cme := replica.GetCustomerManagedEncryption(); cme != nil {
				if key := cme.GetKmsKeyName(); key != "" {
					edges = append(edges, cloud.EdgeSpec{
						SourceID:     secretName,
						TargetID:     key,
						Relationship: kgtypes.EdgeEncryptsWith,
					})
				}
			}
		}
	}

	return edges
}
