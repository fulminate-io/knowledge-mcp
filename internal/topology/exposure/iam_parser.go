// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_parser.go provides a minimal AWS IAM policy document parser used by
// the IAM escalation analyzer. It is intentionally small: only the surface
// area the v1.1 inference rules need (Action/Resource matching, effective
// admin detection, trust principal extraction, condition presence flag).
//
// JSON shape variability is handled by the unmarshallers in
// iam_parser_types.go (string-or-slice fields, "*"-or-struct Principal).
// Policy evaluation (AllowsAction / EvaluateAction / ExplicitlyDenies and
// the ActionDecision enum) lives in iam_parser_eval.go.
//
// The parser is used in three places by the rules:
//
//   - assumeRoleTrustPolicyRule  → extractTrustPolicyFromRoleNode (inline
//     URL-decode + ParseIAMPolicy, in iam_rules_assume.go) + TrustPrincipals
//   - wildcardActionRule         → IsEffectiveAdmin
//   - all other rules            → AllowsAction(action, resource)
//
// Wildcard semantics: AllowsAction supports "*" (everything), prefix
// wildcards like "iam:*" (every iam: action), and suffix wildcards like
// "iam:Attach*" via simple split-and-fragment matching on "*".

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// ParseIAMPolicy parses a raw IAM policy document JSON. Returns a non-nil
// error on any unmarshal failure; never returns a non-nil result on error.
func ParseIAMPolicy(content []byte) (*IAMPolicy, error) {
	if len(content) == 0 {
		return nil, fmt.Errorf("topology/iam_parser: empty policy document")
	}
	var p IAMPolicy
	if err := json.Unmarshal(content, &p); err != nil {
		return nil, fmt.Errorf("topology/iam_parser: parse policy: %w", err)
	}
	return &p, nil
}

// statementMatchesAction returns true if s.Action (or s.NotAction inverse)
// matches the given action.
func statementMatchesAction(s *IAMStatement, action string) bool {
	if len(s.NotAction) > 0 {
		// NotAction matches everything EXCEPT the listed actions.
		for _, na := range s.NotAction {
			if matchPattern(na, action) {
				return false
			}
		}
		return true
	}
	for _, a := range s.Action {
		if matchPattern(a, action) {
			return true
		}
	}
	return false
}

// statementMatchesResource returns true if s.Resource (or s.NotResource
// inverse) matches the given resource ARN.
func statementMatchesResource(s *IAMStatement, resource string) bool {
	if len(s.NotResource) > 0 {
		for _, nr := range s.NotResource {
			if matchPattern(nr, resource) {
				return false
			}
		}
		return true
	}
	for _, r := range s.Resource {
		if matchPattern(r, resource) {
			return true
		}
	}
	return false
}

// matchPattern returns true if pattern matches s. Supports "*" wildcards
// at any position via simple split-and-prefix-suffix matching.
func matchPattern(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return strings.EqualFold(pattern, s)
	}
	parts := strings.Split(pattern, "*")
	rest := s
	for i, part := range parts {
		if part == "" {
			continue
		}
		idx := strings.Index(strings.ToLower(rest), strings.ToLower(part))
		if idx < 0 {
			return false
		}
		if i == 0 && !strings.HasPrefix(pattern, "*") && idx != 0 {
			return false
		}
		rest = rest[idx+len(part):]
	}
	last := parts[len(parts)-1]
	if last != "" && !strings.HasSuffix(pattern, "*") && rest != "" {
		return false
	}
	return true
}

// IsEffectiveAdmin returns true if any statement has Effect=Allow with
// Action="*" and Resource="*" — the strictest definition of admin per
// OQ-4 (no PowerUserAccess, no NotAction tricks).
func (p *IAMPolicy) IsEffectiveAdmin() bool {
	if p == nil {
		return false
	}
	for i := range p.Statements {
		s := &p.Statements[i]
		if !strings.EqualFold(s.Effect, "Allow") {
			continue
		}
		if !containsExact(s.Action, "*") {
			continue
		}
		if !containsExact(s.Resource, "*") {
			continue
		}
		return true
	}
	return false
}

// containsExact returns true if s contains the literal target entry.
func containsExact(slice []string, target string) bool {
	return slices.Contains(slice, target)
}

// TrustPrincipals returns the AWS principal ARNs allowed to assume this
// trust-policy role. Used by assumeRoleTrustPolicyRule. Service principals
// are returned separately by ServicePrincipals.
func (p *IAMPolicy) TrustPrincipals() []string {
	if p == nil {
		return nil
	}
	var out []string
	for i := range p.Statements {
		s := &p.Statements[i]
		if !strings.EqualFold(s.Effect, "Allow") {
			continue
		}
		if s.Principal == nil {
			continue
		}
		if s.Principal.All {
			out = append(out, "*")
			continue
		}
		out = append(out, s.Principal.AWS...)
	}
	return out
}

// ServicePrincipals returns the AWS service principals (e.g.
// "lambda.amazonaws.com", "ec2.amazonaws.com") allowed by trust statements.
// Used by passRoleLambdaRule and runInstancesRule to find roles assumable
// by a given service.
func (p *IAMPolicy) ServicePrincipals() []string {
	if p == nil {
		return nil
	}
	var out []string
	for i := range p.Statements {
		s := &p.Statements[i]
		if !strings.EqualFold(s.Effect, "Allow") {
			continue
		}
		if s.Principal == nil {
			continue
		}
		out = append(out, s.Principal.Service...)
	}
	return out
}

// HasCondition returns true if any Effect=Allow statement carries a
// Condition block. Used to flag inferred edges as conditional.
func (p *IAMPolicy) HasCondition() bool {
	if p == nil {
		return false
	}
	for i := range p.Statements {
		s := &p.Statements[i]
		if !strings.EqualFold(s.Effect, "Allow") {
			continue
		}
		if len(s.Condition) > 0 {
			return true
		}
	}
	return false
}
