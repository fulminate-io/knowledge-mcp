// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_identity_management.go implements the PMapper identity-management
// rules — actions that let a principal manipulate group membership to
// promote itself or hijack another principal's identity. Phase 5 ships one
// rule, with room to grow:
//
//   - addUserToGroupRule — iam:AddUserToGroup. A principal with this action
//     can add ANY user (including itself) to ANY group. Two semantics:
//
//       Admin-group promotion: if the target group has policies that reach
//       effective admin (AdministratorAccess equivalent), the attacker adds
//       itself to that group and is promoted to admin in one hop. We emit
//       a single iamEdgeAttachPolicy self-loop on the attacker per call —
//       the BFS in iam_escalation.go treats attach_policy as automatic
//       admin promotion (matches attachPolicyRule's convention exactly).
//
//       Impersonate fallback: for non-admin groups, the attacker can add
//       any existing user into the group (or itself out of it). The
//       practical capability we model is hijacking the identity of an
//       existing group member by adding them to a group that grants
//       different permissions — which is just impersonation. We emit one
//       iamEdgeImpersonate edge from the attacker to every current member
//       of every non-admin group (skipping self).
//
// Confidence: 1.0, passed via registerIAMRule. Both halves are full one-hop
// captures of the target identity context — adding yourself to an admin
// group and adding someone else to any group are well-documented PMapper
// edges with no conditional dependencies. We reuse hasEffectiveAdminPolicy
// (for the group-reaches-admin check) and principalAllowsAction from the
// shared helpers, plus a per-group member walker that mirrors
// allGroupMembers but scopes to one group instead of all groups in the
// context.

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// addUserToGroupRule emits escalation edges for every iam-user / iam-role
// principal that can call iam:AddUserToGroup. For each target group: if the
// group reaches effective admin via its attached policies, the attacker
// self-promotes via an iamEdgeAttachPolicy self-loop; otherwise the
// attacker can hijack any current member's identity, so we emit
// iamEdgeImpersonate edges to each member (skipping self).
//
// Actor filter: only iam-user and iam-role principals are considered as
// callers. iam-group nodes appear in rctx.Groups for the target side only —
// AWS groups cannot themselves invoke APIs, so emitting an edge FROM a
// group is meaningless. This filter is per-rule, not a v1.1 convention
// change (existing rules iterate every principal type).
func addUserToGroupRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rctx.Groups) == 0 {
		return nil, nil
	}
	actors := make([]*knowledgev1.Node, 0, len(rctx.Users)+len(rctx.Roles))
	actors = append(actors, rctx.Users...)
	actors = append(actors, rctx.Roles...)
	var edges []iamInferredEdge
	for _, p := range actors {
		if !principalAllowsAction(ctx, rctx, p, "iam:AddUserToGroup") {
			continue
		}
		var selfLoopEmitted bool
		for i := range rctx.Groups {
			g := rctx.Groups[i]
			if groupReachesAdmin(ctx, rctx, g) {
				if !selfLoopEmitted {
					edges = append(edges, iamInferredEdge{
						FromID: p.Id,
						ToID:   p.Id,
						Kind:   iamEdgeAttachPolicy,
						Reason: "principal can add itself to an admin group via iam:AddUserToGroup (confidence 1.0)",
					})
					selfLoopEmitted = true
				}
				continue
			}
			edges = append(edges, groupMemberImpersonateEdges(ctx, rctx.scoped, p, g)...)
		}
	}
	return edges, nil
}

// groupReachesAdmin returns true if the group's directly-attached policies
// (inline metadata + managed via EdgeGrants) include any effective-admin
// statement. iterPrincipalPolicies handles inline + managed walking; group
// nodes do not inherit policies from anywhere else (only users walk
// EdgeMemberOf to inherit), so calling iterPrincipalPolicies on a group
// is exactly the right scope here.
func groupReachesAdmin(ctx context.Context, rctx *iamRuleContext, group *knowledgev1.Node) bool {
	return hasEffectiveAdminPolicy(ctx, rctx, group)
}

// groupMemberImpersonateEdges returns one iamEdgeImpersonate edge from
// caller to every current iam-user member of group, skipping self. Members
// are discovered via Forward EdgeHasMember walk on the group ID — the cloud
// collector emits group → user EdgeHasMember per (group, user) pair (see
// cloud/aws/iam_group.go: collectGroupMembers), so this rule walks them
// generically with no cloud/ imports.
func groupMemberImpersonateEdges(
	ctx context.Context, scoped *cloudReader, caller *knowledgev1.Node, group *knowledgev1.Node,
) []iamInferredEdge {
	if scoped == nil {
		return nil
	}
	edges, _ := scoped.iterEdges(ctx, group.Id, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeHasMember})
	var out []iamInferredEdge
	for _, e := range edges {
		if e.ToId == caller.Id {
			continue
		}
		member, err := scoped.nodeByID(ctx, e.ToId)
		if err != nil || member == nil {
			continue
		}
		if resourceTypeOf(member) != "iam-user" {
			continue
		}
		out = append(out, iamInferredEdge{
			FromID: caller.Id,
			ToID:   member.Id,
			Kind:   iamEdgeImpersonate,
			Reason: "principal can add a target user into a group it controls via iam:AddUserToGroup, hijacking their identity (confidence 1.0)",
		})
	}
	return out
}

// init registers the identity-management rules. add_user_to_group is a
// direct admin-equivalent (1.0) per OQ-4: the attacker moves themselves
// into an admin group or adds/removes users to hijack identities.
func init() {
	registerIAMRule("add_user_to_group", 1.0, []string{"iam:AddUserToGroup"}, addUserToGroupRule)
}
