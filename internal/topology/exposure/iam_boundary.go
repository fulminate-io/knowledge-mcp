// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_boundary.go provides the topology-side helpers for AWS IAM permission
// boundary evaluation. The cloud collector (cloud/aws/iam_boundary.go) writes
// the URL-encoded boundary policy document into the principal node metadata
// under the "permission_boundary" key when a role/user has a boundary
// attached and the boundary policy is fetchable. This file reads that key
// back, parses it, and exposes a single boundary-aware action evaluator
// callers can use as the correct AWS semantic for "is this action allowed".
//
// AWS semantics (https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies_boundaries.html):
// when a principal has a permissions boundary, its effective permissions are
// the INTERSECTION of (identity-based policies) and (boundary). Both must
// allow the action; either one denying it (or simply not allowing it) blocks
// the action. Skipping boundaries produces unsound overestimates of
// privilege — false positives on boundary-restricted principals — which is
// why PMapper honors them and v2 of the escalation analyzer must too.
//
// FAIL-CLOSED at this layer: if the metadata key is absent OR the value is
// unparseable as IAM JSON, parseBoundaryPolicy returns nil. nil here means
// "no boundary detected", and the caller (actionAllowedWithinBoundary)
// treats nil as "no restriction" — i.e. it falls back to identity-only
// evaluation. This matches v1.1 behavior and avoids silently downgrading
// permissions when the metadata is just missing. The "fail-closed"
// terminology in the plan refers specifically to the case where a boundary
// IS persisted and IS parseable but does not allow the action: the action
// is denied even if identity allows. That is the case this helper exists to
// handle.
//
// SCOPE: this file ships parseBoundaryPolicy + actionAllowedWithinBoundary
// + unit tests only. It does NOT modify any existing rule. Phase 9 will
// migrate the existing rules to consult the boundary; this step provides
// the building block.

import (
	"net/url"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// permissionBoundaryMetaKey mirrors the constant of the same name in
// cloud/aws/iam_boundary.go. We can't import that package (topology→cloud
// is forbidden by the layering rules), so the literal lives here too. Both
// constants must change together if the wire format ever moves.
const permissionBoundaryMetaKey = "permission_boundary"

// parseBoundaryPolicy reads the "permission_boundary" key from the
// principal's metadata, URL-decodes the document body (the cloud collector
// stores it URL-encoded, mirroring the inline_policy_* convention), and
// parses it as an IAM policy.
//
// Returns nil in any of:
//   - principal has nil metadata
//   - the permission_boundary key is absent
//   - the value is empty
//   - the URL-decoded value fails to parse as an IAM policy JSON document
//
// All four nil cases are semantically equivalent at the call site: "no
// boundary on this principal, do not restrict". This is the v1.1 behavior
// and the v2 fallback when the collector couldn't fetch the boundary.
//
// When parseBoundaryPolicy returns a non-nil *IAMPolicy, the CALLER is
// responsible for intersecting the principal's allowed actions with the
// boundary's allowed actions — see actionAllowedWithinBoundary.
func parseBoundaryPolicy(principal *knowledgev1.Node) *IAMPolicy {
	encoded := nodeMeta(principal, permissionBoundaryMetaKey)
	if encoded == "" {
		return nil
	}
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		// Best-effort: a value that fails URL-decoding might already be
		// raw JSON (the SDK URL-encodes by default but defensive code in
		// the collector could persist a pre-decoded document). Try the
		// raw value before giving up.
		decoded = encoded
	}
	policy, err := ParseIAMPolicy([]byte(decoded))
	if err != nil {
		return nil
	}
	return policy
}

// actionAllowedWithinBoundary returns true if the principal's identity-based
// policies AND (if present) the principal's permissions boundary BOTH allow
// the given (action, resource) tuple. This is the strict AWS semantic.
//
// Boundary semantics:
//
//   - Principal has no boundary metadata → effective permissions == identity
//     policies. Returns true iff at least one identity policy allows the
//     action under standard EvaluateAction semantics (Allow with no
//     overriding Deny).
//   - Principal has a parseable boundary → returns true iff identity allows
//     AND boundary allows. If the boundary is silent on the action (NoMatch)
//     it does NOT allow — boundaries grant nothing on their own; they only
//     constrain. This matches AWS evaluation.
//   - Principal has an unparseable boundary → parseBoundaryPolicy returns
//     nil and we fall through to identity-only evaluation. This is the
//     v1.1-compatible fallback.
//
// The policies argument is the pre-collected slice of identity policies for
// the principal (typically from iterPrincipalPolicies). Passing the slice
// explicitly rather than re-resolving inside this helper keeps the function
// pure and trivially testable, and lets callers reuse the existing
// policy-collection paths unchanged.
//
// All current callers pass resource="*"; the wildcard is inlined here.
// When a non-wildcard boundary check is needed, add the parameter back.
func actionAllowedWithinBoundary(principal *knowledgev1.Node, policies []*IAMPolicy, action string) bool {
	identityAllows := anyPolicyAllows(policies, action, "*")
	if !identityAllows {
		return false
	}
	boundary := parseBoundaryPolicy(principal)
	if boundary == nil {
		return true
	}
	return boundary.AllowsAction(action, "*")
}

// anyPolicyAllows is the helper used by actionAllowedWithinBoundary to test
// whether any identity policy in the slice grants the (action, resource)
// tuple under standard AWS evaluation semantics. Mirrors the loop pattern
// used by principalAllowsAction in iam_rules_passrole.go but stays
// boundary-agnostic so this file remains a self-contained building block.
//
// A nil entry in the slice is treated as "no permissions" (skipped). This
// matches the parseInlinePolicies / parseManagedPolicies contract that
// returns the policies that successfully parsed.
func anyPolicyAllows(policies []*IAMPolicy, action, resource string) bool {
	for _, p := range policies {
		if p == nil {
			continue
		}
		if p.AllowsAction(action, resource) {
			return true
		}
	}
	return false
}
