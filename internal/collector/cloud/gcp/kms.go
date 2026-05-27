// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	iamv1 "cloud.google.com/go/iam/apiv1"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// kmsSubCollector collects Cloud KMS key rings and crypto keys.
// Creates nodes that are the targets of ENCRYPTS_WITH edges emitted
// by other subcollectors (SQL, storage, secrets, bigquery, etc.).
type kmsSubCollector struct {
	client    *kms.KeyManagementClient
	iamClient *iamv1.IamPolicyClient
	projectID string
}

func newKMSSubCollector(
	client *kms.KeyManagementClient,
	iamClient *iamv1.IamPolicyClient,
	projectID string,
) *kmsSubCollector {
	return &kmsSubCollector{client: client, iamClient: iamClient, projectID: projectID}
}

func (c *kmsSubCollector) Name() string { return "gcp-kms" }

func (c *kmsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	// List all key rings across all locations.
	parent := "projects/" + c.projectID + "/locations/-"
	it := c.client.ListKeyRings(ctx, &kmspb.ListKeyRingsRequest{Parent: parent})
	for {
		ring, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}
		if err := c.collectKeyRing(ctx, ring, &result); err != nil {
			return result, err
		}
	}

	return result, nil
}

// collectKeyRing appends the key ring resource and all its crypto keys.
func (c *kmsSubCollector) collectKeyRing(
	ctx context.Context, ring *kmspb.KeyRing, result *cloud.SubCollectorResult,
) error {
	ringName := ring.GetName()
	if ringName == "" {
		return nil
	}

	ringContent, _ := json.Marshal(ring) //nolint:errchkjson // best-effort content envelope
	result.Resources = append(result.Resources, cloud.ResourceSpec{
		ID:           ringName,
		Name:         extractLast(ringName),
		ResourceType: "gcp:cloudkms:keyRing",
		Region:       kmsLocationFromName(ringName),
		Content:      ringContent,
		Metadata:     map[string]string{},
	})

	keyIt := c.client.ListCryptoKeys(ctx, &kmspb.ListCryptoKeysRequest{Parent: ringName})
	for {
		key, err := keyIt.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		keyName := key.GetName()
		if keyName == "" {
			continue
		}

		keyContent, _ := json.Marshal(key) //nolint:errchkjson // best-effort content envelope
		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           keyName,
			Name:         extractLast(keyName),
			ResourceType: "gcp:cloudkms:cryptoKey",
			Region:       kmsLocationFromName(keyName),
			Content:      keyContent,
			Metadata:     cryptoKeyMetadata(key),
		})

		// CONTAINS: keyRing → cryptoKey.
		result.Edges = append(result.Edges, cloud.EdgeSpec{
			SourceID:     ringName,
			TargetID:     keyName,
			Relationship: kgtypes.EdgeContains,
		})

		// Best-effort per-key IAM policy via iam/apiv1.
		if c.iamClient != nil {
			if policy, perr := c.iamClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{
				Resource: keyName,
			}); perr == nil && policy != nil {
				result.Edges = append(result.Edges,
					kmsKeyGrantsEdges(c.projectID, keyName, policy)...)
			} else if perr != nil {
				slog.Debug("gcp-kms: iam policy unavailable",
					"key", keyName, "error", perr)
			}
		}
	}
	return nil
}

// kmsKeyGrantsEdges turns an iampb.Policy into EdgeGrants edges from the
// crypto key to each IAM member. Members are canonicalized to real graph
// node IDs via iamMemberToNodeID so the edges are not dangling against the
// raw "serviceAccount:<email>" / "group:<email>" strings. Pure function for
// testability.
func kmsKeyGrantsEdges(projectID, keyName string, policy *iampb.Policy) []cloud.EdgeSpec {
	if policy == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, binding := range policy.GetBindings() {
		role := binding.GetRole()
		members := make([]string, len(binding.GetMembers()))
		copy(members, binding.GetMembers())
		sort.Strings(members)
		for _, member := range members {
			targetID := iamMemberToNodeID(projectID, member, nil)
			if targetID == "" {
				continue
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     keyName,
				TargetID:     targetID,
				Relationship: kgtypes.EdgeGrants,
				Metadata:     map[string]string{"role": role, "member": member},
			})
		}
	}
	return edges
}

// cryptoKeyMetadata extracts searchable metadata from a CryptoKey.
func cryptoKeyMetadata(key *kmspb.CryptoKey) map[string]string {
	meta := map[string]string{
		"purpose": key.GetPurpose().String(),
	}
	if tmpl := key.GetVersionTemplate(); tmpl != nil {
		meta["algorithm"] = tmpl.GetAlgorithm().String()
		meta["protectionLevel"] = tmpl.GetProtectionLevel().String()
	}
	if rp := key.GetRotationPeriod(); rp != nil {
		meta["rotationPeriod"] = rp.AsDuration().String()
	}
	return meta
}

// kmsLocationFromName extracts the location segment from a KMS resource name
// of the form "projects/P/locations/L/keyRings/R[/cryptoKeys/K]".
func kmsLocationFromName(name string) string {
	_, after, ok := strings.Cut(name, "/locations/")
	if !ok {
		return ""
	}
	location, _, _ := strings.Cut(after, "/")
	return location
}
