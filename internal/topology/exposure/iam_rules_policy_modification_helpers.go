// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_policy_modification_helpers.go holds the principal/policy lookup
// helpers used by the five Put*Policy and *PolicyVersion rules. Split out
// from iam_rules_policy_modification.go to keep both files under the 300-line
// production cap.

import (
	"context"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// awsManagedPolicyPrefix identifies AWS-managed policies. Customers cannot
// create new versions of or set defaults on policies under this prefix.
const awsManagedPolicyPrefix = "arn:aws:iam::aws:policy/"

// allGroupMembers returns every iam-user that belongs to any group in
// rctx.Groups, walked via forward EdgeHasMember (group → user). Duplicates
// are de-duped by ID. The cloud collector emits one EdgeHasMember per
// (group, user) pair (see cloud/aws/iam_group.go: collectGroupMembers).
func allGroupMembers(ctx context.Context, rctx *iamRuleContext) []*knowledgev1.Node {
	if rctx.scoped == nil || len(rctx.Groups) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []*knowledgev1.Node
	for i := range rctx.Groups {
		g := rctx.Groups[i]
		edges, _ := rctx.scoped.iterEdges(ctx, g.Id, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeHasMember})
		for _, e := range edges {
			if seen[e.ToId] {
				continue
			}
			n, err := rctx.scoped.nodeByID(ctx, e.ToId)
			if err != nil || n == nil {
				continue
			}
			if resourceTypeOf(n) != "iam-user" {
				continue
			}
			seen[n.Id] = true
			out = append(out, n)
		}
	}
	return out
}

// principalsWithCustomerManagedPolicies returns every user or role that has
// at least one customer-managed policy attached via EdgeGrants. Groups are
// intentionally excluded: their grants propagate to members via
// iterPrincipalPolicies and the member users are already returned directly.
// Customer-managed vs AWS-managed is decided by ARN prefix — the v1.1
// collector only emits iam-policy nodes for Scope=Local, but test fixtures
// inject AWS-managed ARNs too so the prefix check is load-bearing.
func principalsWithCustomerManagedPolicies(ctx context.Context, rctx *iamRuleContext) []*knowledgev1.Node {
	if rctx.scoped == nil {
		return nil
	}
	seen := make(map[string]bool)
	var out []*knowledgev1.Node
	for _, p := range allPrincipals(rctx) {
		rt := resourceTypeOf(p)
		if rt != "iam-user" && rt != "iam-role" {
			continue
		}
		if seen[p.Id] {
			continue
		}
		if holdsCustomerManagedPolicy(ctx, rctx.scoped, p.Id) {
			seen[p.Id] = true
			out = append(out, p)
		}
	}
	return out
}

// holdsCustomerManagedPolicy returns true if the principal has any outgoing
// EdgeGrants edge whose target ARN is NOT under the AWS-managed prefix.
func holdsCustomerManagedPolicy(ctx context.Context, scoped *cloudReader, principalID string) bool {
	if scoped == nil || principalID == "" {
		return false
	}
	edges, _ := scoped.iterEdges(ctx, principalID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeGrants})
	for _, e := range edges {
		if !strings.HasPrefix(e.ToId, awsManagedPolicyPrefix) {
			return true
		}
	}
	return false
}
