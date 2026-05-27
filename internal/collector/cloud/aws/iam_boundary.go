// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"log/slog"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// iam_boundary.go fetches AWS IAM permission-boundary policies and persists
// the URL-encoded document on the principal node so the topology side can
// evaluate effective permissions as the intersection of identity policies
// AND the boundary.
//
// AWS semantics (https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies_boundaries.html):
// when a role or user has a PermissionsBoundary set, the maximum permissions
// of that principal are the intersection of (identity-based policies) and
// (boundary). Skipping boundaries produces unsound overestimates.
//
// FAIL-OPEN at the collector layer: if GetPolicy or GetPolicyVersion fails
// for any reason — IAM permission denied, network error, deleted boundary
// policy — we log a warning and continue without persisting the boundary
// field. The topology side then sees no boundary and does not restrict the
// principal: the topology layer fails *closed* on a parsed but
// over-restrictive boundary, but a missing boundary is treated as "no
// restriction" by both layers.

// permissionBoundaryMetaKey is the metadata key under which the URL-encoded
// boundary policy document is persisted. The topology parser
// (parseBoundaryPolicy in topology/iam_boundary.go) reads this exact key.
const permissionBoundaryMetaKey = "permission_boundary"

// permissionBoundaryArnMetaKey is the metadata key under which the boundary
// policy ARN is persisted for debugging. Topology rules do not read this
// key directly — they only need the document — but it makes debugging
// easier when looking at a node by hand.
const permissionBoundaryArnMetaKey = "permission_boundary_arn"

// boundaryFetcher is the narrow IAM API surface needed to resolve a boundary
// ARN to its current default-version document. The concrete *iam.Client
// implements both methods; tests extend fakeIAMAPI to match.
type boundaryFetcher interface {
	GetPolicy(ctx context.Context, params *iam.GetPolicyInput, optFns ...func(*iam.Options)) (*iam.GetPolicyOutput, error)
	GetPolicyVersion(ctx context.Context, params *iam.GetPolicyVersionInput, optFns ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error)
}

// boundaryCache memoizes boundary lookups within a single Collect() call so
// multiple roles/users sharing the same boundary policy ARN incur exactly
// one (GetPolicy + GetPolicyVersion) round trip. The cached value is the
// URL-encoded document body as returned by IAM (empty string == fetch
// failed, do not retry within this collection pass).
type boundaryCache struct {
	mu sync.Mutex
	// docs maps boundary ARN → URL-encoded document body. An entry with an
	// empty value means the lookup was attempted and failed; the failure is
	// remembered to avoid retrying the same broken ARN once per principal.
	docs map[string]string
}

// newBoundaryCache returns an empty cache ready for use.
func newBoundaryCache() *boundaryCache {
	return &boundaryCache{docs: map[string]string{}}
}

// fetchBoundaryDocument resolves a boundary policy ARN to its URL-encoded
// default-version document. Memoized per arn within the cache. On any
// failure (client doesn't implement boundaryFetcher, GetPolicy errors,
// missing default version, GetPolicyVersion errors) the function logs and
// returns an empty string — the caller treats that as "no boundary
// persisted" and continues without restriction.
func (c *boundaryCache) fetchBoundaryDocument(ctx context.Context, client iamAPI, arn string) string {
	if arn == "" {
		return ""
	}

	c.mu.Lock()
	if doc, ok := c.docs[arn]; ok {
		c.mu.Unlock()
		return doc
	}
	c.mu.Unlock()

	doc := resolveBoundaryDocument(ctx, client, arn)

	c.mu.Lock()
	c.docs[arn] = doc
	c.mu.Unlock()
	return doc
}

// resolveBoundaryDocument performs the actual two-step IAM API dance:
//
//  1. GetPolicy(arn) → discover DefaultVersionId
//  2. GetPolicyVersion(arn, versionID) → fetch URL-encoded document body
//
// Returns the document body on success or an empty string on any failure
// (with a structured warning logged via slog). The caller is responsible
// for treating the empty case as "no boundary collected".
func resolveBoundaryDocument(ctx context.Context, client iamAPI, arn string) string {
	bf, ok := client.(boundaryFetcher)
	if !ok {
		slog.Warn("iam: boundary fetcher unsupported by client",
			"arn", arn,
			"reason", "client does not implement GetPolicy/GetPolicyVersion")
		return ""
	}

	policyOut, err := bf.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: awssdk.String(arn)})
	if err != nil {
		slog.Warn("iam: boundary GetPolicy failed (fail-open, principal will be collected without boundary)",
			"arn", arn, "error", err)
		return ""
	}
	if policyOut == nil || policyOut.Policy == nil || policyOut.Policy.DefaultVersionId == nil {
		slog.Warn("iam: boundary policy has no default version (fail-open)", "arn", arn)
		return ""
	}

	versionOut, err := bf.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{
		PolicyArn: awssdk.String(arn),
		VersionId: policyOut.Policy.DefaultVersionId,
	})
	if err != nil {
		slog.Warn("iam: boundary GetPolicyVersion failed (fail-open)",
			"arn", arn,
			"version_id", awssdk.ToString(policyOut.Policy.DefaultVersionId),
			"error", err)
		return ""
	}
	if versionOut == nil || versionOut.PolicyVersion == nil || versionOut.PolicyVersion.Document == nil {
		slog.Warn("iam: boundary policy version has no document (fail-open)", "arn", arn)
		return ""
	}
	return awssdk.ToString(versionOut.PolicyVersion.Document)
}

// applyBoundaryMetadata writes the boundary ARN and URL-encoded document into
// the given metadata map (allocating if needed) when both are present. Empty
// arn or empty doc are no-ops — the caller's "fail-open" path lands here. The
// returned map is the same one passed in (or a new map if nil).
func applyBoundaryMetadata(meta map[string]string, arn, encodedDoc string) map[string]string {
	if arn == "" || encodedDoc == "" {
		return meta
	}
	if meta == nil {
		meta = make(map[string]string, 2)
	}
	meta[permissionBoundaryArnMetaKey] = arn
	meta[permissionBoundaryMetaKey] = encodedDoc
	return meta
}
