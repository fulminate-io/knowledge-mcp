// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_parser_trust.go implements trust-policy principal matching with
// NotPrincipal support. Split out from iam_parser.go to keep each file
// under the 300-line soft cap.
//
// AWS trust-policy semantics (reference:
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_notprincipal.html):
//
//  1. Principal matches when the caller's ARN is listed (or Principal=="*"
//     means "any principal in the world").
//  2. NotPrincipal matches when the caller's ARN is NOT listed. Combined
//     with Effect=Allow this means "every principal in the account EXCEPT
//     the listed ones can assume this role" — effectively wide-open.
//  3. Effect=Deny+NotPrincipal inverts the same way: "deny everyone EXCEPT
//     the listed principals" — an inverted allow-list expressed as a deny.
//  4. AWS explicitly disallows having both Principal and NotPrincipal in a
//     single statement. We treat such malformed statements as no-match
//     rather than guessing.
//  5. AWS explicitly disallows wildcards ("*") inside NotPrincipal entries;
//     NotPrincipal values must be literal ARNs. We still tolerate "*" in
//     decoded input by treating All=true on NotPrincipal as "excludes
//     everyone", i.e. the statement grants access to no one.
//
// The Evaluate/Allow flow mirrors iam_parser_eval.go:
//   - Any matching Deny short-circuits to refusal.
//   - Otherwise any matching Allow returns true.
//   - Otherwise false.
//
// This file exposes two new IAMPolicy methods:
//
//   - TrustPrincipalMatches(arn) — can `arn` assume the role under the
//     trust policy, honoring both Principal and NotPrincipal inversions.
//   - IsTrustPolicyWideOpen() — reports whether any Allow statement uses
//     NotPrincipal (making the role effectively wide-open) and the union
//     of excluded principal ARNs across all such statements.
//
// Rule-layer flagging of wide-open trust policies is Phase 6 work and
// lives in iam_rules_*.go, not here. This file only exposes the parser
// capability.

import "strings"

// TrustPrincipalMatches returns true if the given caller ARN is permitted
// to assume the role under this trust policy. Honors both Principal
// (inclusive) and NotPrincipal (exclusive) per AWS semantics, and applies
// Deny-overrides-Allow evaluation.
//
// Semantics:
//
//   - Allow + Principal contains arn (or Principal=="*")          → Allow match
//   - Allow + NotPrincipal set AND does NOT contain arn           → Allow match (wide-open except list)
//   - Deny  + Principal contains arn (or Principal=="*")          → Deny match
//   - Deny  + NotPrincipal set AND does NOT contain arn           → Deny match (inverted deny)
//
// If the same statement has both Principal and NotPrincipal set, it is
// treated as no-match — AWS rejects such statements and guessing is worse
// than being strict. Statements without either are also no-match.
func (p *IAMPolicy) TrustPrincipalMatches(arn string) bool {
	if p == nil || arn == "" {
		return false
	}
	anyAllow := false
	for i := range p.Statements {
		s := &p.Statements[i]
		if !trustStatementMatchesARN(s, arn) {
			continue
		}
		if strings.EqualFold(s.Effect, "Deny") {
			return false
		}
		if strings.EqualFold(s.Effect, "Allow") {
			anyAllow = true
		}
	}
	return anyAllow
}

// trustStatementMatchesARN returns true if the statement (either Allow or
// Deny) applies to the given caller ARN. Encapsulates Principal /
// NotPrincipal / both-set / neither-set logic in one place so Allow and
// Deny are evaluated identically.
func trustStatementMatchesARN(s *IAMStatement, arn string) bool {
	hasP := s.Principal != nil
	hasN := s.NotPrincipal != nil
	// AWS rejects statements that set both; treat as no-match.
	if hasP && hasN {
		return false
	}
	if hasP {
		return principalContains(s.Principal, arn)
	}
	if hasN {
		// NotPrincipal with All=true excludes everyone: no match.
		if s.NotPrincipal.All {
			return false
		}
		return !principalContains(s.NotPrincipal, arn)
	}
	// Neither set: not a trust statement (or implicit in identity policies).
	return false
}

// principalContains returns true if the given principal block names the
// caller ARN. Honors the "*" All flag for wide-open grants and matches
// AWS-style wildcard patterns inside AWS principal entries via matchPattern.
func principalContains(p *IAMPrincipal, arn string) bool {
	if p == nil {
		return false
	}
	if p.All {
		return true
	}
	for _, pattern := range p.AWS {
		if pattern == "*" || matchPattern(pattern, arn) {
			return true
		}
	}
	return false
}

// IsTrustPolicyWideOpen reports whether any Effect=Allow statement in the
// trust policy uses NotPrincipal — meaning the role can be assumed by
// every principal in the account (or world) except the listed ones.
//
// Return contract:
//
//   - wideOpen == true: at least one Allow statement has NotPrincipal set
//     (and is not the degenerate NotPrincipal="*" case that excludes
//     everyone). The except slice holds the union of excluded principal
//     ARNs across every such statement, deduplicated and in first-seen
//     order. If the wildcard "*" appears in any NotPrincipal it will be
//     included in except verbatim — callers may choose to display it.
//   - wideOpen == false: no Allow statement has a meaningful NotPrincipal;
//     except is nil.
//
// Deny+NotPrincipal statements are intentionally ignored here — they are
// not wide-open grants. The Phase 6 rule layer consumes this method to
// emit "trust policy is wide-open (except: alice, bob)" findings with
// critical severity.
func (p *IAMPolicy) IsTrustPolicyWideOpen() (bool, []string) {
	if p == nil {
		return false, nil
	}
	var except []string
	seen := make(map[string]struct{})
	wideOpen := false
	for i := range p.Statements {
		s := &p.Statements[i]
		if !strings.EqualFold(s.Effect, "Allow") {
			continue
		}
		if s.NotPrincipal == nil {
			continue
		}
		// Both-set statements are malformed per AWS; skip them so we don't
		// report a false wide-open based on a statement AWS would reject.
		if s.Principal != nil {
			continue
		}
		// NotPrincipal=="*" excludes everyone → statement grants nothing,
		// not wide-open.
		if s.NotPrincipal.All {
			continue
		}
		wideOpen = true
		for _, arn := range s.NotPrincipal.AWS {
			if _, ok := seen[arn]; ok {
				continue
			}
			seen[arn] = struct{}{}
			except = append(except, arn)
		}
		// Service / Federated exclusions are surfaced too — the rule layer
		// may want to know which services are excluded from an otherwise
		// wide-open grant.
		for _, svc := range s.NotPrincipal.Service {
			key := "service:" + svc
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			except = append(except, svc)
		}
		for _, fed := range s.NotPrincipal.Federated {
			key := "federated:" + fed
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			except = append(except, fed)
		}
	}
	if !wideOpen {
		return false, nil
	}
	return true, except
}
