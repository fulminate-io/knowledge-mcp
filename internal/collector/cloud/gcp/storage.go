// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"cloud.google.com/go/iam"
	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// storageSubCollector collects GCS buckets and related edges.
type storageSubCollector struct {
	client    *gcs.Client
	projectID string
}

func newStorageSubCollector(client *gcs.Client, projectID string) *storageSubCollector {
	return &storageSubCollector{client: client, projectID: projectID}
}

func (c *storageSubCollector) Name() string { return "gcp-storage" }

// Collect iterates every bucket in the project and emits one resource +
// edges per bucket. Per-bucket IAM policy and notifications are fetched
// via separate RPCs (bucket.IAM().Policy / bucket.Notifications) because
// BucketAttrs does not carry either field.
func (c *storageSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.Buckets(ctx, c.projectID)
	seenProxies := map[string]bool{}

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		bucket, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}
		if bucket.Name == "" {
			continue
		}

		content, err := json.Marshal(bucket)
		if err != nil {
			continue
		}

		// GCS bucket IDs use gs:// canonical URL format (decision 1390ea2b).
		bucketID := "gs://" + bucket.Name

		spec := cloud.ResourceSpec{
			ID:           bucketID,
			Name:         bucket.Name,
			ResourceType: "gcp:storage:bucket",
			Region:       bucket.Location,
			Content:      content,
			Metadata: map[string]string{
				"storageClass": bucket.StorageClass,
			},
		}
		result.Resources = append(result.Resources, spec)

		// Edges that depend only on BucketAttrs: encryption + log sink.
		result.Edges = append(result.Edges, storageBucketEdges(bucketID, bucket)...)

		// Best-effort IAM + notifications (separate RPCs; ignore errors).
		handle := c.client.Bucket(bucket.Name)
		c.collectBucketIAMAndNotifications(ctx, handle, bucketID, bucket.Name,
			seenProxies, &result)
	}

	return result, nil
}

// storageBucketEdges returns edges derived from BucketAttrs alone. These
// are the edges that do not require additional RPCs:
//
//   - EdgeEncryptsWith when CMEK (Customer-Managed Encryption Key) is set.
//   - EdgeSinksTo when bucket Logging.LogBucket is configured — the source
//     bucket ships access logs to the destination bucket.
func storageBucketEdges(bucketID string, bucket *gcs.BucketAttrs) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	if bucket.Encryption != nil && bucket.Encryption.DefaultKMSKeyName != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     bucketID,
			TargetID:     bucket.Encryption.DefaultKMSKeyName,
			Relationship: kgtypes.EdgeEncryptsWith,
			Metadata:     map[string]string{"encryption_scope": "bucket"},
		})
	}

	if bucket.Logging != nil && bucket.Logging.LogBucket != "" {
		meta := map[string]string{}
		if bucket.Logging.LogObjectPrefix != "" {
			meta["log_object_prefix"] = bucket.Logging.LogObjectPrefix
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     bucketID,
			TargetID:     "gs://" + bucket.Logging.LogBucket,
			Relationship: kgtypes.EdgeSinksTo,
			Metadata:     meta,
		})
	}

	return edges
}

// storageBucketGrantsEdges turns an already-fetched bucket IAM policy into
// one EdgeGrants per (role, member) pair. The edge direction is
// bucket → member, consistent with the GCP IAM audit config model where
// the bucket "grants" the role to the member. The role is captured in edge
// metadata under "role" so downstream analyzers can reason about permission
// scopes.
func storageBucketGrantsEdges(bucketID string, policy *iam.Policy) []cloud.EdgeSpec {
	if policy == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	// Sort roles for deterministic output (policy.Roles() returns them in
	// proto order, which depends on server-side ordering).
	roles := policy.Roles()
	roleStrs := make([]string, 0, len(roles))
	for _, r := range roles {
		roleStrs = append(roleStrs, string(r))
	}
	sort.Strings(roleStrs)

	for _, role := range roleStrs {
		members := policy.Members(iam.RoleName(role))
		sort.Strings(members)
		for _, member := range members {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     bucketID,
				TargetID:     member,
				Relationship: kgtypes.EdgeGrants,
				Metadata:     map[string]string{"role": role},
			})
		}
	}
	return edges
}

// storageBucketNotifyEdges converts a map of storage.Notification into
// EdgeTriggers edges from the bucket to the Pub/Sub topic referenced by
// the notification. The seen set is used across the collector run to
// dedupe uncollected-target proxy nodes so we only emit one proxy per
// topic resource name.
//
// Topic IDs are normalised to the canonical
// projects/{project}/topics/{topic} form. If the notification target is a
// topic we have not collected yet (cross-project, or we simply haven't
// indexed Pub/Sub), the function also emits an uncollected proxy node per
// OQ-H.
func storageBucketNotifyEdges(
	bucketID string,
	notifs map[string]*gcs.Notification,
	seenProxies map[string]bool,
) ([]cloud.EdgeSpec, []cloud.ResourceSpec) {
	if len(notifs) == 0 {
		return nil, nil
	}
	// Sort notification IDs for determinism across map iteration.
	ids := make([]string, 0, len(notifs))
	for id := range notifs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var edges []cloud.EdgeSpec
	var proxies []cloud.ResourceSpec
	for _, id := range ids {
		n := notifs[id]
		if n == nil || n.TopicID == "" || n.TopicProjectID == "" {
			continue
		}
		topicID := fmt.Sprintf("projects/%s/topics/%s", n.TopicProjectID, n.TopicID)
		meta := map[string]string{
			"notification_id": n.ID,
			"payload_format":  n.PayloadFormat,
		}
		if len(n.EventTypes) > 0 {
			meta["event_types"] = strings.Join(n.EventTypes, ",")
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     bucketID,
			TargetID:     topicID,
			Relationship: kgtypes.EdgeTriggers,
			Metadata:     meta,
		})

		if !seenProxies[topicID] {
			seenProxies[topicID] = true
			proxies = append(proxies, cloud.ResourceSpec{
				ID:           topicID,
				Name:         n.TopicID,
				ResourceType: "gcp:pubsub:topic",
				Metadata: map[string]string{
					"collected":        "false",
					"collected_reason": "no collector registered",
					"discovered_via":   "storage notification",
				},
			})
		}
	}
	return edges, proxies
}

// collectBucketIAMAndNotifications performs best-effort IAM policy and
// Pub/Sub notification lookups for a single bucket (separate RPCs).
func (c *storageSubCollector) collectBucketIAMAndNotifications(
	ctx context.Context, handle *gcs.BucketHandle, bucketID, bucketName string,
	seenProxies map[string]bool, result *cloud.SubCollectorResult,
) {
	if handle == nil {
		return
	}
	if policy, err := handle.IAM().Policy(ctx); err == nil && policy != nil {
		result.Edges = append(result.Edges,
			storageBucketGrantsEdges(bucketID, policy)...)
	} else if err != nil {
		slog.Debug("gcp-storage: iam policy unavailable",
			"bucket", bucketName, "error", err)
	}
	if notifs, err := handle.Notifications(ctx); err == nil {
		edges, proxies := storageBucketNotifyEdges(bucketID, notifs, seenProxies)
		result.Edges = append(result.Edges, edges...)
		result.Resources = append(result.Resources, proxies...)
	} else {
		slog.Debug("gcp-storage: notifications unavailable",
			"bucket", bucketName, "error", err)
	}
}
