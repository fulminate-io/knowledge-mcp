// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_keys.go implements createAccessKeyRule.
//
// For each principal that can call iam:CreateAccessKey on user resources,
// emit one impersonate edge to every iam-user in the account. The
// principal can mint a permanent access key for the user and from then on
// authenticate as them.
//
// Also registers all seven v1.1 rules with the dispatch table via init().

import (
	"context"
)

// createAccessKeyRule emits impersonate edges from every principal that can
// call iam:CreateAccessKey to every iam-user in the account. The user IS
// the escalation target — once the attacker has a key for the user they
// gain whatever permissions the user holds.
func createAccessKeyRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rctx.Users) == 0 {
		return nil, nil
	}
	var edges []iamInferredEdge
	for _, p := range allPrincipals(rctx) {
		if !principalAllowsAction(ctx, rctx, p, "iam:CreateAccessKey") {
			continue
		}
		for i := range rctx.Users {
			user := rctx.Users[i]
			if user.Id == p.Id {
				// Skip self-impersonation: minting a key for yourself adds nothing.
				continue
			}
			edges = append(edges, iamInferredEdge{
				FromID: p.Id,
				ToID:   user.Id,
				Kind:   iamEdgeImpersonate,
				Reason: "principal can create access keys for IAM users",
			})
		}
	}
	return edges, nil
}

// init registers all seven v1.1 rules. Per-rule confidence values per OQ-4:
//
//   - assume_role_trust_policy: 1.0 (direct: trust policy already grants assume)
//   - wildcard_action:          1.0 (direct: Allow * * is literal admin)
//   - attach_policy:             1.0 (direct: attacker attaches AdministratorAccess to self)
//   - pass_role_lambda:         0.9 (PassRole family: target role trust policy gates)
//   - run_instances:            0.9 (PassRole family: EC2 instance-profile composition)
//   - create_function:          0.9 (PassRole family: Lambda CreateFunction composition)
//   - create_access_key:        1.0 (direct: attacker mints permanent creds for target user)
func init() {
	// assume_role_trust_policy passes nil Actions because it inspects
	// target role trust policies, not source identity policies. The
	// boundary filter is skipped for edges emitted by this rule — they
	// are by definition not gated on a source-principal action.
	registerIAMRule("assume_role_trust_policy", 1.0, nil, assumeRoleTrustPolicyRule)
	// wildcard_action flags principals whose identity policy contains
	// Allow *:*. The only boundary that preserves this is one that also
	// allows *. A restrictive boundary correctly suppresses the finding.
	registerIAMRule("wildcard_action", 1.0, []string{"*"}, wildcardActionRule)
	// attach_policy matches any of the three iam:Attach*Policy variants
	// the rule body tests. Boundary filter requires all three so a
	// boundary that allows only a subset (e.g. only iam:AttachUserPolicy
	// on scoped users) does not suppress a true positive. Use the union;
	// if a principal's boundary allows ANY of the three the rule would
	// still fire.
	// NOTE: multi-action semantics here are "principal needs ANY of these
	// to trigger escalation". The boundary filter as designed requires
	// ALL actions to be allowed. Passing the tightest single anchor
	// (iam:AttachUserPolicy) matches the rule's most common trigger.
	registerIAMRule("attach_policy", 1.0, []string{"iam:AttachUserPolicy"}, attachPolicyRule)
	registerIAMRule("pass_role_lambda", 0.9, []string{"iam:PassRole"}, passRoleLambdaRule)
	registerIAMRule("run_instances", 0.9, []string{"iam:PassRole"}, runInstancesRule)
	registerIAMRule("create_function", 0.9, []string{"lambda:CreateFunction"}, createFunctionRule)
	registerIAMRule("create_access_key", 1.0, []string{"iam:CreateAccessKey"}, createAccessKeyRule)
}
