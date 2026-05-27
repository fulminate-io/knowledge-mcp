// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"net/url"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iam_boundary_test.go covers the parseBoundaryPolicy and
// actionAllowedWithinBoundary helpers in iam_boundary.go. These tests are
// pure-unit (no DB, no fixture) — they construct knowledgev1.Node literals with
// the metadata shape produced by cloud/aws/iam_boundary.go and exercise the
// helpers directly.

// boundaryDocS3Star is the canonical permissive boundary used across the
// allow-path tests. It allows every s3 action on every resource and nothing
// else, so any non-s3 identity allow is suppressed by the boundary.
const boundaryDocS3Star = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`

// principalWithBoundary returns a knowledgev1.Node with the URL-encoded
// boundaryDocS3Star document persisted under the permission_boundary metadata
// key — exactly the shape the AWS collector emits. Every caller wants the
// same canonical permissive boundary, so the doc is hardcoded; if a future
// test needs a different boundary shape, reintroduce the parameter.
func principalWithBoundary() *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: "arn:aws:iam::111111111111:role/test",
		Metadata: map[string]string{
			permissionBoundaryMetaKey: url.QueryEscape(boundaryDocS3Star),
		},
	}
}

// principalWithRawMetadata is for tests that need to inject malformed or
// missing metadata values directly without URL encoding.
func principalWithRawMetadata(meta map[string]string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id:       "arn:aws:iam::111111111111:role/test",
		Metadata: meta,
	}
}

// mustParsePolicy is a test helper that parses an IAM policy JSON string or
// fails the test on parse error. Used to build identity policies for the
// actionAllowedWithinBoundary tests.
func mustParsePolicy(t *testing.T, doc string) *IAMPolicy {
	t.Helper()
	p, err := ParseIAMPolicy([]byte(doc))
	require.NoError(t, err, "fixture policy must parse")
	return p
}

// --- parseBoundaryPolicy ---------------------------------------------------

// TestParseBoundaryPolicy_NoBoundary verifies the absence path: a principal
// with no metadata at all and a principal with metadata that lacks the
// permission_boundary key both return nil.
func TestParseBoundaryPolicy_NoBoundary(t *testing.T) {
	// Nil metadata.
	got := parseBoundaryPolicy(&knowledgev1.Node{Id: "arn:aws:iam::1:role/no-meta"})
	assert.Nil(t, got, "nil metadata must return nil")

	// Metadata present but no permission_boundary key.
	other := principalWithRawMetadata(map[string]string{"arn": "x", "name": "y"})
	got = parseBoundaryPolicy(other)
	assert.Nil(t, got, "metadata without permission_boundary key must return nil")
}

// TestParseBoundaryPolicy_Parses verifies the happy path: a principal with a
// permission_boundary key holding a URL-encoded valid IAM policy returns a
// parsed *IAMPolicy whose AllowsAction reflects the boundary contents.
func TestParseBoundaryPolicy_Parses(t *testing.T) {
	got := parseBoundaryPolicy(principalWithBoundary())
	require.NotNil(t, got, "valid URL-encoded boundary must parse")
	assert.True(t, got.AllowsAction("s3:GetObject", "*"), "boundary should allow s3:GetObject")
	assert.False(t, got.AllowsAction("ec2:RunInstances", "*"), "boundary should not allow ec2:RunInstances")
}

// TestParseBoundaryPolicy_Unparseable verifies the fail-closed path: a
// permission_boundary value that is not valid IAM JSON returns nil — same
// as the no-boundary case. The caller treats nil as "no restriction" so
// the action falls back to identity-only evaluation, matching v1.1
// behavior and avoiding silent over-restriction.
func TestParseBoundaryPolicy_Unparseable(t *testing.T) {
	got := parseBoundaryPolicy(principalWithRawMetadata(map[string]string{
		permissionBoundaryMetaKey: "not-json-at-all-{{{",
	}))
	assert.Nil(t, got, "unparseable boundary must return nil")

	// Empty value also returns nil (collector path: applyBoundaryMetadata
	// short-circuits on empty doc, but defend against the edge case anyway).
	got = parseBoundaryPolicy(principalWithRawMetadata(map[string]string{
		permissionBoundaryMetaKey: "",
	}))
	assert.Nil(t, got, "empty boundary value must return nil")
}

// --- actionAllowedWithinBoundary -------------------------------------------

// TestActionAllowedWithinBoundary_NoBoundary_OnlyIdentity verifies that when
// the principal has no boundary, the helper degrades to identity-only
// evaluation: identity allows → return true; identity does not allow →
// return false.
func TestActionAllowedWithinBoundary_NoBoundary_OnlyIdentity(t *testing.T) {
	identity := mustParsePolicy(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:RunInstances","Resource":"*"}]}`)
	principal := &knowledgev1.Node{Id: "arn:aws:iam::1:role/no-bnd"}

	allowed := actionAllowedWithinBoundary(principal, []*IAMPolicy{identity}, "ec2:RunInstances")
	assert.True(t, allowed, "identity allows + no boundary → allowed")

	notAllowed := actionAllowedWithinBoundary(principal, []*IAMPolicy{identity}, "ec2:TerminateInstances")
	assert.False(t, notAllowed, "identity does not allow + no boundary → not allowed")
}

// TestActionAllowedWithinBoundary_BoundaryDenies verifies that when identity
// allows the action but the boundary does not include it, the action is
// blocked. (AWS boundaries restrict via intersection — silence in the
// boundary equals "not in the intersection".)
func TestActionAllowedWithinBoundary_BoundaryDenies(t *testing.T) {
	identity := mustParsePolicy(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:RunInstances","Resource":"*"}]}`)
	principal := principalWithBoundary() // s3:* only

	allowed := actionAllowedWithinBoundary(principal, []*IAMPolicy{identity}, "ec2:RunInstances")
	assert.False(t, allowed, "boundary lacks ec2:RunInstances → action blocked even though identity allows")
}

// TestActionAllowedWithinBoundary_BoundaryAllows_IdentityDenies verifies
// that the boundary cannot grant permissions on its own. If the identity
// policy does not allow the action, the action is blocked regardless of
// what the boundary permits.
func TestActionAllowedWithinBoundary_BoundaryAllows_IdentityDenies(t *testing.T) {
	identity := mustParsePolicy(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:RunInstances","Resource":"*"}]}`)
	principal := principalWithBoundary() // s3:* only

	allowed := actionAllowedWithinBoundary(principal, []*IAMPolicy{identity}, "s3:GetObject")
	assert.False(t, allowed, "boundary allows s3:GetObject but identity does not → action blocked")
}

// TestActionAllowedWithinBoundary_BothAllow verifies the happy path: when
// identity AND boundary both allow the same action, the helper returns true.
func TestActionAllowedWithinBoundary_BothAllow(t *testing.T) {
	identity := mustParsePolicy(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	principal := principalWithBoundary()

	allowed := actionAllowedWithinBoundary(principal, []*IAMPolicy{identity}, "s3:GetObject")
	assert.True(t, allowed, "identity allows AND boundary allows → action permitted")
}

// TestActionAllowedWithinBoundary_BothDeny verifies that when neither
// identity nor boundary allow the action, the result is false. (Trivial
// case but exercises the early-return path on identityAllows == false.)
func TestActionAllowedWithinBoundary_BothDeny(t *testing.T) {
	identity := mustParsePolicy(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	principal := principalWithBoundary()

	allowed := actionAllowedWithinBoundary(principal, []*IAMPolicy{identity}, "iam:CreateUser")
	assert.False(t, allowed, "neither identity nor boundary allow iam:CreateUser → action blocked")
}
