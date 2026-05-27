// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_parser_conditions_string.go holds the String* and Arn* condition
// operator evaluators. ARN operators in AWS IAM behave like string
// operators with slightly different wildcard semantics — ArnEquals is a
// case-sensitive exact match, ArnLike permits '*' and '?' wildcards. We
// route both families through a shared wildcard matcher.
//
// Wildcard semantics (StringLike, ArnLike):
//   '*' — zero or more characters
//   '?' — exactly one character
//
// StringEquals is a case-sensitive literal match. StringEqualsIgnoreCase
// lowercases both sides before comparing.

import "strings"

// evalStringOperator dispatches the 6 string operators against specValues
// and the already-resolved ctxValues. For multi-valued spec, the policy
// says any spec value that matches is enough for an "equals-family"
// comparison; the "not-equals" family requires that none of the spec
// values match.
func evalStringOperator(op string, specValues, ctxValues []string) bool {
	switch op {
	case "StringEquals":
		return anyPairMatches(specValues, ctxValues, stringEquals)
	case "StringNotEquals":
		return allPairsDiffer(specValues, ctxValues, stringEquals)
	case "StringEqualsIgnoreCase":
		return anyPairMatches(specValues, ctxValues, stringEqualsIgnoreCase)
	case "StringNotEqualsIgnoreCase":
		return allPairsDiffer(specValues, ctxValues, stringEqualsIgnoreCase)
	case "StringLike":
		return anyPairMatches(specValues, ctxValues, stringLike)
	case "StringNotLike":
		return allPairsDiffer(specValues, ctxValues, stringLike)
	}
	return false
}

// evalArnOperator dispatches the 4 Arn* operators. AWS IAM's ArnLike
// accepts the same '*' / '?' wildcard alphabet as StringLike; ArnEquals
// is an exact case-sensitive match. The Not variants invert the result.
func evalArnOperator(op string, specValues, ctxValues []string) bool {
	switch op {
	case "ArnEquals":
		return anyPairMatches(specValues, ctxValues, stringEquals)
	case "ArnNotEquals":
		return allPairsDiffer(specValues, ctxValues, stringEquals)
	case "ArnLike":
		return anyPairMatches(specValues, ctxValues, stringLike)
	case "ArnNotLike":
		return allPairsDiffer(specValues, ctxValues, stringLike)
	}
	return false
}

// anyPairMatches returns true if ANY (specValue, ctxValue) pair satisfies
// the supplied predicate. This is the "equals-family" semantic — matching
// one spec value against the context key is a match.
func anyPairMatches(specValues, ctxValues []string, pred func(spec, ctx string) bool) bool {
	for _, cv := range ctxValues {
		for _, sv := range specValues {
			if pred(sv, cv) {
				return true
			}
		}
	}
	return false
}

// allPairsDiffer returns true if NO (specValue, ctxValue) pair satisfies
// the supplied predicate. This is the "not-equals" semantic — a single
// hit anywhere fails the whole block.
func allPairsDiffer(specValues, ctxValues []string, pred func(spec, ctx string) bool) bool {
	for _, cv := range ctxValues {
		for _, sv := range specValues {
			if pred(sv, cv) {
				return false
			}
		}
	}
	return true
}

// stringEquals is a case-sensitive literal string compare.
func stringEquals(spec, ctx string) bool {
	return spec == ctx
}

// stringEqualsIgnoreCase lowercases both sides before comparison.
func stringEqualsIgnoreCase(spec, ctx string) bool {
	return strings.EqualFold(spec, ctx)
}

// stringLike matches spec against ctx with '*' (zero-or-more) and '?'
// (exactly-one) wildcards. No escaping; AWS IAM does not document an
// escape syntax for these wildcards in condition values.
func stringLike(spec, ctx string) bool {
	return wildcardMatch(spec, ctx)
}

// wildcardMatch runs a recursive-free, linear-time backtracking match
// supporting '*' and '?'. The algorithm is the standard two-pointer
// wildcard matcher (O(n*m) worst-case, O(n) typical).
func wildcardMatch(pattern, s string) bool {
	var (
		p, ip      = 0, 0  // pattern index / last '*' pattern index
		si, isMark = 0, -1 // source index / last '*' source index snapshot
	)
	for si < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			ip = p
			isMark = si
			p++
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[si]):
			p++
			si++
		case isMark >= 0:
			// Backtrack to the last '*' and advance the source.
			p = ip + 1
			isMark++
			si = isMark
		default:
			return false
		}
	}
	// Consume trailing '*' in the pattern.
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
