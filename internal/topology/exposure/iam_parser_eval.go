// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_parser_eval.go implements AWS-semantic policy evaluation: whether a
// policy grants or explicitly denies a given (action, resource) request.
// Split out from iam_parser.go to keep each file under the 300-line cap.
//
// AWS IAM evaluation logic (reference:
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic.html):
//
//  1. Any matching Effect=Deny statement wins, unconditionally. Explicit
//     deny always overrides any allow, regardless of policy order.
//  2. Otherwise, any matching Effect=Allow statement grants access.
//  3. Otherwise, the request is denied by default.
//
// The three-valued result is exposed via ActionDecision so callers can tell
// "no statement matched" apart from "explicitly denied" — useful for rules
// that want to treat those cases differently. AllowsAction keeps the
// existing bool contract (true ⇔ Allow) for backward compatibility.
//
// Wildcard / NotAction / NotResource handling is delegated to the shared
// statementMatchesAction / statementMatchesResource helpers in iam_parser.go.

import "strings"

// ActionDecision is the outcome of evaluating a policy against a single
// (action, resource) tuple. It mirrors AWS IAM's three-valued evaluation
// result: no matching statement, an explicit allow, or an explicit deny.
type ActionDecision int

// ActionDecision values.
const (
	// NoMatch means no statement matched the action+resource tuple.
	NoMatch ActionDecision = iota
	// Allow means at least one Allow statement matched and no Deny did.
	Allow
	// ExplicitDeny means at least one Deny statement matched — overrides any Allow.
	ExplicitDeny
)

// String renders an ActionDecision for logs and test failure messages.
func (d ActionDecision) String() string {
	switch d {
	case NoMatch:
		return "NoMatch"
	case Allow:
		return "Allow"
	case ExplicitDeny:
		return "ExplicitDeny"
	default:
		return "Unknown"
	}
}

// EvaluateAction returns the AWS-semantic decision for (action, resource).
// It walks every statement, tracking whether any Allow matched, and returns
// ExplicitDeny immediately on the first matching Deny (explicit deny is
// unconditional and short-circuits evaluation).
//
// Wildcard handling and NotAction/NotResource inversion are the same as
// AllowsAction. Pass empty string for resource to ignore the resource match.
func (p *IAMPolicy) EvaluateAction(action, resource string) ActionDecision {
	if p == nil {
		return NoMatch
	}
	anyAllow := false
	for i := range p.Statements {
		s := &p.Statements[i]
		if !statementMatchesActionAndResource(s, action, resource) {
			continue
		}
		if strings.EqualFold(s.Effect, "Deny") {
			return ExplicitDeny
		}
		if strings.EqualFold(s.Effect, "Allow") {
			anyAllow = true
		}
	}
	if anyAllow {
		return Allow
	}
	return NoMatch
}

// AllowsAction returns true if the policy grants the given (action, resource)
// under AWS semantics: at least one Allow statement matches AND no Deny
// statement matches. Wildcard handling:
//
//   - Action="*" matches every action.
//   - Action="iam:*" matches every iam: action.
//   - Action="iam:Attach*" matches "iam:AttachUserPolicy", "iam:AttachRolePolicy".
//   - Resource="*" matches every resource.
//   - Resource patterns ending in "*" act as prefix matches.
//
// NotAction / NotResource statements are evaluated as inverse matches.
// Pass empty string for resource to ignore the resource match (i.e. match
// any statement that allows the action regardless of resource).
//
// Explicit Deny is honored: an Allow statement that would otherwise grant
// access is overridden by any matching Deny statement. This is the standard
// AWS IAM evaluation logic.
func (p *IAMPolicy) AllowsAction(action, resource string) bool {
	return p.EvaluateAction(action, resource) == Allow
}

// ExplicitlyDenies returns true if any Deny statement in the policy matches
// the given (action, resource). Useful for rules that need to distinguish
// "explicitly denied" from "silently not allowed" — for example, a condition
// evaluator that wants to skip Allow-statement inspection entirely when the
// action is explicitly denied.
func (p *IAMPolicy) ExplicitlyDenies(action, resource string) bool {
	return p.EvaluateAction(action, resource) == ExplicitDeny
}

// statementMatchesActionAndResource is the shared predicate used by
// EvaluateAction to decide whether a statement applies to the request. It
// combines action matching, resource matching, and the empty-resource
// wildcard shortcut in one place so Allow and Deny statements are evaluated
// identically.
func statementMatchesActionAndResource(s *IAMStatement, action, resource string) bool {
	if !statementMatchesAction(s, action) {
		return false
	}
	if resource != "" && !statementMatchesResource(s, resource) {
		return false
	}
	return true
}

// EvaluateActionWithContext is the condition-aware counterpart to
// EvaluateAction. It walks every statement, and for each statement that
// matches the action+resource it additionally consults EvaluateCondition
// against the supplied ConditionContext. A statement whose condition block
// does not match is skipped entirely — as though it did not exist — so a
// Deny with a failing condition does NOT explicitly deny the request, and
// an Allow with a failing condition does NOT count toward the anyAllow
// tally.
//
// This is the strict PMapper-parity evaluator. Callers that want the v1.1
// permissive "conditions always pass" semantic should keep calling
// EvaluateAction, which ignores conditions entirely.
//
// The returned ActionDecision uses the same three-valued semantics as
// EvaluateAction: NoMatch / Allow / ExplicitDeny.
func (p *IAMPolicy) EvaluateActionWithContext(action, resource string, cctx ConditionContext) ActionDecision {
	if p == nil {
		return NoMatch
	}
	anyAllow := false
	for i := range p.Statements {
		s := &p.Statements[i]
		if !statementMatchesActionAndResource(s, action, resource) {
			continue
		}
		if !statementConditionMatches(s, cctx) {
			continue
		}
		if strings.EqualFold(s.Effect, "Deny") {
			return ExplicitDeny
		}
		if strings.EqualFold(s.Effect, "Allow") {
			anyAllow = true
		}
	}
	if anyAllow {
		return Allow
	}
	return NoMatch
}

// AllowsActionWithContext is the condition-aware counterpart to
// AllowsAction. Returns true iff EvaluateActionWithContext returns Allow.
func (p *IAMPolicy) AllowsActionWithContext(action, resource string, cctx ConditionContext) bool {
	return p.EvaluateActionWithContext(action, resource, cctx) == Allow
}

// statementConditionMatches returns true if the statement has no Condition
// block, or if its Condition block evaluates true against cctx. An absent
// or empty Condition is vacuously true — this mirrors AWS IAM grammar and
// keeps the no-condition case O(1).
func statementConditionMatches(s *IAMStatement, cctx ConditionContext) bool {
	if len(s.Condition) == 0 {
		return true
	}
	return EvaluateCondition(s.Condition, cctx)
}
