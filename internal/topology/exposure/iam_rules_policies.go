// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_policies.go holds the policy-enumeration helpers used by every
// rule that needs to answer "what does this principal effectively allow?"
// — inline policies, managed policies, and (for users) group-inherited
// policies. Split out from iam_rules.go to keep both files under the
// 300-line production cap.

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// iterPrincipalPolicies returns every IAM policy attached to the given
// principal: inline policies (metadata key "inline_policy_<name>"), managed
// policies (via EdgeGrants forward), and — for users — their groups' policies
// (via EdgeMemberOf forward → group → its inline + managed policies).
//
// This is the heart of the wildcardActionRule, passRoleLambdaRule, and other
// rules that need to evaluate "what does this principal effectively allow?"
// across the entire policy attachment chain.
//
// Results are memoized in rctx.policyCache for the lifetime of the Run.
// dispatchIAMRules invokes ~26 rules per Run, and most of them call
// iterPrincipalPolicies for every principal they inspect — without the cache
// each principal's inline + managed policies are JSON-parsed ~26x. The
// 2026-04-09 baseline profile attributed ~26% of CPU and ~32% of allocations
// on the iam_large fixture to this redundant parsing.
//
// The returned slice MUST NOT be mutated by callers — it is shared across
// every rule that asks about the same principal in the same Run. Every
// in-tree caller iterates with `for _, policy := range ...` and only calls
// read-only methods on *IAMPolicy, so this contract is currently honored
// everywhere; any future caller that needs to mutate must copy first.
func iterPrincipalPolicies(ctx context.Context, rctx *iamRuleContext, principal *knowledgev1.Node) []*IAMPolicy {
	if rctx != nil && rctx.policyCache != nil {
		rctx.policyCacheMu.RLock()
		cached, ok := rctx.policyCache[principal.Id]
		rctx.policyCacheMu.RUnlock()
		if ok {
			return cached
		}
	}
	out := collectDirectPolicies(ctx, rctx.scoped, principal)
	if isUser(principal) {
		// Group inheritance: a user effectively allows whatever its groups allow.
		groups := resolveUserGroups(ctx, rctx.scoped, principal.Id)
		for _, g := range groups {
			out = append(out, collectDirectPolicies(ctx, rctx.scoped, g)...)
		}
	}
	if rctx != nil && rctx.policyCache != nil && principal.Id != "" {
		rctx.policyCacheMu.Lock()
		rctx.policyCache[principal.Id] = out
		rctx.policyCacheMu.Unlock()
	}
	return out
}

// collectDirectPolicies returns every inline + managed policy directly
// attached to the given principal (no group walking).
func collectDirectPolicies(ctx context.Context, scoped *cloudReader, principal *knowledgev1.Node) []*IAMPolicy {
	out := parseInlinePolicies(principal)
	managed := parseManagedPolicies(ctx, scoped, principal.Id)
	return append(out, managed...)
}

// parseInlinePolicies parses every inline_policy_<name> metadata entry on a
// principal node. Inline policy values are URL-encoded JSON per the collector
// contract. Failures are silently skipped — a malformed policy on one node
// must not break the entire analysis pass.
func parseInlinePolicies(principal *knowledgev1.Node) []*IAMPolicy {
	if principal.Metadata == nil {
		return nil
	}
	var out []*IAMPolicy
	for k, v := range principal.Metadata {
		if !strings.HasPrefix(k, "inline_policy_") {
			continue
		}
		// Inline policy values are URL-encoded JSON (per cloud/aws/iam_*.go).
		decoded, err := url.QueryUnescape(v)
		if err != nil {
			decoded = v
		}
		p, err := ParseIAMPolicy([]byte(decoded))
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// parseManagedPolicies follows every outgoing EdgeGrants edge from a
// principal to an iam-policy node, extracts the policy document from the
// managedPolicyContent envelope in node.Content, URL-decodes it, and parses.
// Returns a slice of every successfully-parsed managed policy.
func parseManagedPolicies(ctx context.Context, scoped *cloudReader, principalID string) []*IAMPolicy {
	if scoped == nil || principalID == "" {
		return nil
	}
	edges, _ := scoped.iterEdges(ctx, principalID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeGrants})
	var out []*IAMPolicy
	for _, e := range edges {
		policyNode, err := scoped.nodeByID(ctx, e.ToId)
		if err != nil || policyNode == nil {
			continue
		}
		p := parseManagedPolicyEnvelope(policyNode.Content)
		if p != nil {
			out = append(out, p)
		}
	}
	return out
}

// parseManagedPolicyEnvelope extracts and parses the URL-encoded policy
// document inside a managedPolicyContent envelope. Returns nil on any
// failure — the caller treats missing policies as "no permissions" rather
// than aborting the entire run.
//
// The envelope shape is defined as managedPolicyContent in cloud/aws/iam_policy.go
// (a curated projection of iamtypes.Policy with explicit JSON tags, mirroring
// acmCertificateContent at cloud/aws/acm.go:201). We can't import that package
// (topology→cloud is forbidden), so we use a local struct that matches the
// JSON tag on the `document` field only — every other field on the wire
// envelope is intentionally ignored here. If you need additional fields,
// extend this local struct (and add a build-tag-free unit test that locks the
// JSON tag against cloud/aws/iam_policy.go's struct definition).
func parseManagedPolicyEnvelope(content string) *IAMPolicy {
	if content == "" {
		return nil
	}
	var envelope struct {
		Document string `json:"document"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil
	}
	if envelope.Document == "" {
		return nil
	}
	decoded, err := url.QueryUnescape(envelope.Document)
	if err != nil {
		decoded = envelope.Document
	}
	p, err := ParseIAMPolicy([]byte(decoded))
	if err != nil {
		return nil
	}
	return p
}

// resolveUserGroups returns the iam-group nodes a user belongs to, walked
// via outgoing EdgeMemberOf edges.
func resolveUserGroups(ctx context.Context, scoped *cloudReader, userID string) []*knowledgev1.Node {
	if scoped == nil {
		return nil
	}
	edges, _ := scoped.iterEdges(ctx, userID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeMemberOf})
	var out []*knowledgev1.Node
	for _, e := range edges {
		gn, err := scoped.nodeByID(ctx, e.ToId)
		if err != nil || gn == nil {
			continue
		}
		out = append(out, gn)
	}
	return out
}

// resourceTypeOf returns the cloud resource_type metadata of a node, or
// empty if absent. Used by the rule helpers to filter to iam-user, iam-role,
// iam-group, etc.
func resourceTypeOf(n *knowledgev1.Node) string {
	return nodeMeta(n, "resource_type")
}

// isUser returns true if the node is a cloud iam-user.
func isUser(n *knowledgev1.Node) bool { return resourceTypeOf(n) == "iam-user" }
