// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_credential_access.go implements two PMapper credential-theft
// rules that let a principal hijack an existing IAM user's identity by
// stamping a new console-login credential onto the user and then
// authenticating as them.
//
//   - createLoginProfileRule — iam:CreateLoginProfile. A principal with
//     this action can set a console password on any iam-user that has no
//     login profile yet, then sign in as that user.
//
//   - updateLoginProfileRule — iam:UpdateLoginProfile. A principal with
//     this action can reset the console password of any iam-user that
//     already has a login profile, then sign in as that user.
//
// Both rules emit iamEdgeImpersonate edges from the caller to every
// iam-user in the account (minus self). The v1.1 collector does not yet
// record whether a user has an active login profile, so we cannot filter
// the target set at this layer. Mirrors createAccessKeyRule's behavior in
// iam_rules_keys.go. When the collector gains a "has_login_profile"
// metadata flag, partitioning this set is a one-line change.
//
// Confidence: both rules are full admin impersonation of the target user
// (the attacker fully controls the target identity afterward) — confidence
// 1.0, passed via the registerIAMRule call.

import (
	"context"
)

// impersonateAllUsersRule is the shared body for credential-access rules
// that let a principal hijack every iam-user in the account by stamping
// fresh credentials onto the target. It enumerates every principal, keeps
// the ones that allow the given action, and emits one impersonate edge
// per (caller, user) pair, skipping self-impersonation (minting or
// resetting credentials for yourself adds nothing the caller does not
// already have).
func impersonateAllUsersRule(
	ctx context.Context,
	rctx *iamRuleContext,
	action string,
	reason string,
) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rctx.Users) == 0 {
		return nil, nil
	}
	var edges []iamInferredEdge
	for _, p := range allPrincipals(rctx) {
		if !principalAllowsAction(ctx, rctx, p, action) {
			continue
		}
		for i := range rctx.Users {
			user := rctx.Users[i]
			if user.Id == p.Id {
				// Skip self-impersonation — resetting your own
				// credentials adds nothing.
				continue
			}
			edges = append(edges, iamInferredEdge{
				FromID: p.Id,
				ToID:   user.Id,
				Kind:   iamEdgeImpersonate,
				Reason: reason,
			})
		}
	}
	return edges, nil
}

// createLoginProfileRule emits impersonate edges from every principal that
// can call iam:CreateLoginProfile to every iam-user in the account. A
// principal with this action can set a console password on any user that
// lacks one, then authenticate as that user through the AWS console.
// Confidence: 1.0 (full admin impersonation of the target identity).
func createLoginProfileRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return impersonateAllUsersRule(
		ctx,
		rctx,
		"iam:CreateLoginProfile",
		"principal can create a console login profile for IAM users",
	)
}

// updateLoginProfileRule emits impersonate edges from every principal that
// can call iam:UpdateLoginProfile to every iam-user in the account. A
// principal with this action can reset the console password of any user
// that already has a login profile, then authenticate as that user.
// Confidence: 1.0 (full admin impersonation of the target identity).
func updateLoginProfileRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return impersonateAllUsersRule(
		ctx,
		rctx,
		"iam:UpdateLoginProfile",
		"principal can reset the console password of IAM users",
	)
}

// init registers the two credential-access rules. createAccessKeyRule
// stays registered in iam_rules_keys.go (OQ-9 minimum churn). Both rules
// are direct admin-equivalent (confidence 1.0 per OQ-4): a principal that
// stamps a fresh login profile on a target user fully controls that user.
func init() {
	registerIAMRule("create_login_profile", 1.0, []string{"iam:CreateLoginProfile"}, createLoginProfileRule)
	registerIAMRule("update_login_profile", 1.0, []string{"iam:UpdateLoginProfile"}, updateLoginProfileRule)
}
