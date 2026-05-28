// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules.go defines the rule registry, the iamRule function shape, and
// the shared types every rule depends on (iamInferredEdge, iamRuleContext,
// iterPrincipalPolicies). Individual rules live in iam_rules_*.go so each
// file stays under the 300-line cap.
//
// OQ-5 (public API): every symbol here is package-private. OQ-2
// (cross-account): iamRuleContext carries both the per-account scoped reader
// and the raw wire caller so assumeRoleTrustPolicyRule can resolve
// cross-account principals.

import (
	"context"
	"fmt"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// iamInferredEdgeKind classifies what kind of escalation edge a rule emitted.
// The escalation analyzer's BFS uses these to decide whether reaching the
// target counts as admin promotion.
type iamInferredEdgeKind string

// iamInferredEdgeKind constants — one per v1.1 rule output type.
const (
	iamEdgeAssumeRole     iamInferredEdgeKind = "assume_role"
	iamEdgeExecuteAs      iamInferredEdgeKind = "execute_as"
	iamEdgeImpersonate    iamInferredEdgeKind = "impersonate"
	iamEdgeAttachPolicy   iamInferredEdgeKind = "attach_policy"
	iamEdgeEffectiveAdmin iamInferredEdgeKind = "effective_admin"
)

// iamInferredEdge is one inferred escalation edge between principals (or a
// self-loop annotating effective admin / attach policy capability). When
// ToID == FromID the edge is a marker on the principal itself — used by
// effective_admin and attach_policy self-promotions.
//
// Confidence (Phase 9 Step 1, OQ-4) is a 0..1 score set from the
// registering rule's per-rule constant by dispatchIAMRules. Rules MAY
// override by emitting a non-zero Confidence directly; dispatchIAMRules
// only populates the field when it is zero. RuleName records the emitting
// rule so Step 3 dedup can enumerate contributors.
//
// Account (Phase 9.5, OQ-7) is the AWS account ID that owns the FROM
// principal — i.e., the account whose policies the rule evaluated when
// it emitted this edge. Cross-account walks set this to the source
// account's ID so the BFS can key its visited set on (Account, ID)
// tuples and continue expanding across account boundaries. Rules that
// don't set Account explicitly have it stamped by dispatchIAMRules from
// rctx.Account (the currently-dispatching account).
type iamInferredEdge struct {
	FromID      string
	ToID        string
	Account     string
	Kind        iamInferredEdgeKind
	Reason      string
	Conditional bool
	Confidence  float64
	RuleName    string
}

// iamRuleContext carries everything a rule needs to evaluate the v1.1
// principal set in one account. Pre-fetched principal slices avoid the
// O(rules * principals) duplicate query that would otherwise dominate runtime.
//
// Cross-account walks use the raw wire caller (foundation.FetchGraphNames +
// foundation.FetchNodeByID against another account). Rules that don't need
// cross-account access can ignore caller and read through scoped (the
// per-account cloud reader).
type iamRuleContext struct {
	caller   foundation.GraphCaller
	scoped   *cloudReader
	Account  string
	Roles    []*knowledgev1.Node
	Users    []*knowledgev1.Node
	Groups   []*knowledgev1.Node
	Policies []*knowledgev1.Node
	// Functions are the lambda-function nodes in the scoped account, used by
	// the iam_rules_lambda_update.go rules to enumerate the per-function
	// execution-role target set. Unlike Roles/Users/Groups, the BFS does not
	// treat lambda-function nodes as principals; they are pure target
	// resolvers for the UpdateFunctionCode escalation shape.
	Functions []*knowledgev1.Node
	// policyCache memoizes the fully-resolved []*IAMPolicy returned by
	// iterPrincipalPolicies, keyed by principal node ID. Each Run constructs
	// a fresh iamRuleContext (one per account in dispatchAcrossAccounts), so
	// the cache is naturally Run-scoped — no explicit clear needed.
	//
	// The cache exists because the dispatcher invokes ~26 rules per Run and
	// every rule that gates on identity policies calls iterPrincipalPolicies
	// for every principal it inspects. Without the cache the same role's
	// inline + managed policies are JSON-parsed ~26x per Run, which the
	// 2026-04-09 baseline profile measured at ~26% of CPU and ~32% of
	// allocations on the iam_large fixture.
	//
	// Cache values are NOT defensively copied. Verified by grep that every
	// iterPrincipalPolicies caller iterates the slice with `for _, policy
	// := range ...` and calls only read-only methods on *IAMPolicy
	// (IsEffectiveAdmin, AllowsAction, AllowsActionWithContext). If a
	// future caller needs to mutate the slice or its policies, copy at
	// the call site.
	policyCache   map[string][]*IAMPolicy
	policyCacheMu sync.RWMutex // guards policyCache during parallel rule dispatch
}

// iamRule is the rule function shape. Each rule scans rctx for matching
// patterns and returns the inferred edges it discovered. Rules must respect
// ctx cancellation and must not mutate any graph.
type iamRule func(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error)

// iamRuleEntry bundles a rule's function with its per-rule confidence
// constant (OQ-4) and the set of AWS actions it requires the source
// principal to be allowed to perform. dispatchIAMRules stamps Confidence
// onto every edge the rule emits that left Confidence at zero, and uses
// Actions to apply permission-boundary filtering: an edge is dropped if
// the source principal has a parseable boundary that does NOT allow one
// or more of the actions in Actions.
//
// Actions captures what the rule's own identity-side check (e.g.
// principalAllowsAction) tests — it MUST match the action strings the
// rule body passes to AllowsAction, or the boundary filter will mask
// false positives the rule never flagged in the first place. For
// multi-action rules (e.g. ecs_run_task needs both ecs:RunTask AND
// iam:PassRole), list every action; the boundary check requires every
// listed action to be allowed by the boundary.
//
// For the wildcard_action rule, Actions is []string{"*"} — the only
// action that an "Allow *" identity policy implies the principal can
// use. A boundary that does not allow "*" correctly rejects the self
// promotion.
//
// For assume_role_trust_policy, Actions is nil. That rule inspects
// target role trust policies, not source identity policies, so there
// is no identity-side action to gate on. dispatchFilterByBoundary
// treats nil Actions as "rule is not boundary-filterable" and passes
// its edges through unchanged.
type iamRuleEntry struct {
	Fn         iamRule
	Confidence float64
	Actions    []string
}

// iamRules is the package-level rule dispatch table. Rules self-register
// at init() time via registerIAMRule.
var (
	iamRulesMu sync.RWMutex
	iamRules   = map[string]iamRuleEntry{}
)

// registerIAMRule adds a rule to the dispatch table. Panics on nil rule,
// empty name, out-of-range confidence ((0, 1]), or duplicate registration
// — all programmer errors that should never reach a running server.
//
// actions lists every AWS action the rule's source-principal identity
// check tests (passed to principalAllowsAction / AllowsAction inside the
// rule body). The dispatcher uses this list to apply permission-boundary
// filtering before accepting the rule's emitted edges. Pass nil for rules
// that do not gate on source identity (e.g. assume_role_trust_policy,
// which inspects target role trust policies).
func registerIAMRule(name string, confidence float64, actions []string, rule iamRule) {
	if rule == nil {
		panic("topology: registerIAMRule called with nil iamRule")
	}
	if name == "" {
		panic("topology: registerIAMRule called with empty name")
	}
	if confidence <= 0 || confidence > 1 {
		panic(fmt.Sprintf("topology: registerIAMRule %q: confidence must be in (0, 1], got %v", name, confidence))
	}
	iamRulesMu.Lock()
	defer iamRulesMu.Unlock()
	if _, dup := iamRules[name]; dup {
		panic(fmt.Sprintf("topology: duplicate IAM rule registration: %q", name))
	}
	iamRules[name] = iamRuleEntry{Fn: rule, Confidence: confidence, Actions: actions}
}

// lookupIAMRule lives in iam_rules_testonly_test.go — it is only exercised
// from the same-package test files. Moved there so `deadcode ./...` does not
// flag it as an unreachable production function.

// lookupIAMRuleEntry returns the full entry (fn + confidence) for a rule.
// Used by dispatchIAMRules to stamp confidence onto emitted edges.
func lookupIAMRuleEntry(name string) (iamRuleEntry, bool) {
	iamRulesMu.RLock()
	defer iamRulesMu.RUnlock()
	r, ok := iamRules[name]
	return r, ok
}

// allIAMRules returns a snapshot of the registered rule names. Used by the
// escalation analyzer to iterate the full rule set.
func allIAMRules() []string {
	iamRulesMu.RLock()
	defer iamRulesMu.RUnlock()
	out := make([]string, 0, len(iamRules))
	for name := range iamRules {
		out = append(out, name)
	}
	return out
}

// iterPrincipalPolicies and the shared policy-parsing helpers
// (collectDirectPolicies, parseInlinePolicies, parseManagedPolicies,
// parseManagedPolicyEnvelope, resolveUserGroups, resourceTypeOf, isUser)
// live in iam_rules_policies.go to keep this file under the 300-line
// production cap.
