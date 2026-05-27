// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_parser_conditions_test.go is the table-driven test suite for the
// PMapper-core condition operator evaluator. Every operator has at least
// one positive and one negative case; wildcard / quantifier / IfExists
// edge cases are covered in dedicated tests.

import (
	"net"
	"testing"
	"time"
)

// TestIAMConditions_ParseOperator covers the modifier parser — stripping
// IfExists suffix and ForAllValues / ForAnyValue prefixes.
func TestIAMConditions_ParseOperator(t *testing.T) {
	cases := []struct {
		op         string
		wantBase   string
		wantIf     bool
		wantForAll bool
		wantForAny bool
	}{
		{"StringEquals", "StringEquals", false, false, false},
		{"StringEqualsIfExists", "StringEquals", true, false, false},
		{"ForAllValues:StringLike", "StringLike", false, true, false},
		{"ForAnyValue:StringEquals", "StringEquals", false, false, true},
		{"ForAllValues:StringEqualsIfExists", "StringEquals", true, true, false},
		{"ForAnyValue:StringLikeIfExists", "StringLike", true, false, true},
	}
	for _, tc := range cases {
		base, mod := parseOperator(tc.op)
		if base != tc.wantBase || mod.IfExists != tc.wantIf ||
			mod.ForAllValues != tc.wantForAll || mod.ForAnyValue != tc.wantForAny {
			t.Errorf("parseOperator(%q) = (%q, %+v), want (%q, IfExists=%v ForAllValues=%v ForAnyValue=%v)",
				tc.op, base, mod, tc.wantBase, tc.wantIf, tc.wantForAll, tc.wantForAny)
		}
	}
}

// TestIAMConditions_StringOperators covers all 6 string operators with a
// positive and negative pair each.
func TestIAMConditions_StringOperators(t *testing.T) {
	ctx := ConditionContext{
		PrincipalArn: "arn:aws:iam::123456789012:user/alice",
	}
	cases := []struct {
		name string
		cond map[string]any
		want bool
	}{
		{"StringEquals match",
			cond("StringEquals", "aws:PrincipalArn", "arn:aws:iam::123456789012:user/alice"), true},
		{"StringEquals mismatch",
			cond("StringEquals", "aws:PrincipalArn", "arn:aws:iam::123456789012:user/bob"), false},
		{"StringNotEquals miss (match is false)",
			cond("StringNotEquals", "aws:PrincipalArn", "arn:aws:iam::123456789012:user/alice"), false},
		{"StringNotEquals differ",
			cond("StringNotEquals", "aws:PrincipalArn", "arn:aws:iam::123456789012:user/bob"), true},
		{"StringEqualsIgnoreCase",
			cond("StringEqualsIgnoreCase", "aws:PrincipalArn", "ARN:AWS:IAM::123456789012:USER/ALICE"), true},
		{"StringNotEqualsIgnoreCase",
			cond("StringNotEqualsIgnoreCase", "aws:PrincipalArn", "ARN:AWS:IAM::123456789012:USER/BOB"), true},
		{"StringLike prefix wildcard",
			cond("StringLike", "aws:PrincipalArn", "arn:aws:iam::123456789012:user/*"), true},
		{"StringLike question mark",
			cond("StringLike", "aws:PrincipalArn", "arn:aws:iam::123456789012:user/alic?"), true},
		{"StringLike no match",
			cond("StringLike", "aws:PrincipalArn", "arn:aws:iam::123456789012:role/*"), false},
		{"StringNotLike match inverts",
			cond("StringNotLike", "aws:PrincipalArn", "arn:aws:iam::123456789012:role/*"), true},
		{"StringNotLike hit inverts to false",
			cond("StringNotLike", "aws:PrincipalArn", "arn:aws:iam::*:user/alice"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateCondition(tc.cond, ctx); got != tc.want {
				t.Errorf("EvaluateCondition = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIAMConditions_StringLike covers wildcard edge cases for the
// stringLike matcher. The criterion TestIAMConditions_StringLike must
// exercise both '*' and '?' plus anchored/unanchored behavior.
func TestIAMConditions_StringLike(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*", "anything", true},
		{"abc*", "abcdef", true},
		{"*abc", "xyzabc", true},
		{"*abc*", "xyabcde", true},
		{"a?c", "abc", true},
		{"a?c", "abbc", false},
		{"a*z", "az", true},
		{"a*z", "aquickbrownfoxz", true},
		{"a*z", "abcz0", false},
		{"prefix*suffix", "prefix-middle-suffix", true},
		{"prefix*suffix", "prefixsuffix", true},
		{"prefix*suffix", "prefix-suffix-nope", false},
		{"exact", "exact", true},
		{"exact", "Exact", false},
		{"", "", true},
		{"", "nonempty", false},
	}
	for _, tc := range cases {
		if got := stringLike(tc.pattern, tc.s); got != tc.want {
			t.Errorf("stringLike(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

// TestIAMConditions_BoolOperator covers true / false / missing-key paths.
func TestIAMConditions_BoolOperator(t *testing.T) {
	mfaOn := ConditionContext{MFAPresent: true}
	mfaOff := ConditionContext{MFAPresent: false}
	if !EvaluateCondition(cond("Bool", "aws:MultiFactorAuthPresent", "true"), mfaOn) {
		t.Error("Bool true should match MFAPresent=true")
	}
	if EvaluateCondition(cond("Bool", "aws:MultiFactorAuthPresent", "true"), mfaOff) {
		t.Error("Bool true should NOT match MFAPresent=false")
	}
	if !EvaluateCondition(cond("Bool", "aws:MultiFactorAuthPresent", "false"), mfaOff) {
		t.Error("Bool false should match MFAPresent=false")
	}
	// Unknown custom key → default-deny (key absent).
	if EvaluateCondition(cond("Bool", "aws:ViaAWSService", "true"), mfaOn) {
		t.Error("missing context key should default-deny")
	}
}

// TestIAMConditions_NumericOperators covers all 6 numeric operators.
func TestIAMConditions_NumericOperators(t *testing.T) {
	ctx := ConditionContext{Extras: map[string][]string{"custom:count": {"5"}}}
	cases := []struct {
		op   string
		spec string
		want bool
	}{
		{"NumericEquals", "5", true},
		{"NumericEquals", "6", false},
		{"NumericNotEquals", "6", true},
		{"NumericNotEquals", "5", false},
		{"NumericLessThan", "10", true},
		{"NumericLessThan", "5", false},
		{"NumericLessThanEquals", "5", true},
		{"NumericLessThanEquals", "4", false},
		{"NumericGreaterThan", "4", true},
		{"NumericGreaterThan", "5", false},
		{"NumericGreaterThanEquals", "5", true},
		{"NumericGreaterThanEquals", "6", false},
	}
	for _, tc := range cases {
		got := EvaluateCondition(cond(tc.op, "custom:count", tc.spec), ctx)
		if got != tc.want {
			t.Errorf("%s 5 %s = %v, want %v", tc.op, tc.spec, got, tc.want)
		}
	}
}

// TestIAMConditions_DateOperators covers all 6 date operators.
func TestIAMConditions_DateOperators(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2024-06-15T12:00:00Z")
	ctx := ConditionContext{Now: now}
	cases := []struct {
		op   string
		spec string
		want bool
	}{
		{"DateEquals", "2024-06-15T12:00:00Z", true},
		{"DateEquals", "2024-06-15T12:00:01Z", false},
		{"DateNotEquals", "2024-06-15T12:00:01Z", true},
		{"DateNotEquals", "2024-06-15T12:00:00Z", false},
		{"DateLessThan", "2024-12-31T00:00:00Z", true},
		{"DateLessThan", "2023-01-01T00:00:00Z", false},
		{"DateLessThanEquals", "2024-06-15T12:00:00Z", true},
		{"DateLessThanEquals", "2024-06-15T11:59:59Z", false},
		{"DateGreaterThan", "2024-01-01T00:00:00Z", true},
		{"DateGreaterThan", "2024-12-31T00:00:00Z", false},
		{"DateGreaterThanEquals", "2024-06-15T12:00:00Z", true},
		{"DateGreaterThanEquals", "2024-06-15T12:00:01Z", false},
	}
	for _, tc := range cases {
		got := EvaluateCondition(cond(tc.op, "aws:CurrentTime", tc.spec), ctx)
		if got != tc.want {
			t.Errorf("%s ctx=%s spec=%s = %v, want %v", tc.op, now.Format(time.RFC3339), tc.spec, got, tc.want)
		}
	}
}

// TestIAMConditions_IPOperators covers IpAddress and NotIpAddress with
// CIDR and bare-IP spec values.
func TestIAMConditions_IPOperators(t *testing.T) {
	ctx := ConditionContext{SourceIP: net.ParseIP("192.0.2.42")}
	cases := []struct {
		op   string
		spec string
		want bool
	}{
		{"IpAddress", "192.0.2.0/24", true},
		{"IpAddress", "10.0.0.0/8", false},
		{"IpAddress", "192.0.2.42", true},
		{"IpAddress", "192.0.2.43", false},
		{"NotIpAddress", "10.0.0.0/8", true},
		{"NotIpAddress", "192.0.2.0/24", false},
	}
	for _, tc := range cases {
		got := EvaluateCondition(cond(tc.op, "aws:SourceIp", tc.spec), ctx)
		if got != tc.want {
			t.Errorf("%s spec=%s = %v, want %v", tc.op, tc.spec, got, tc.want)
		}
	}
}

// TestIAMConditions_ArnOperators covers ArnEquals / ArnLike and their Not
// variants.
func TestIAMConditions_ArnOperators(t *testing.T) {
	ctx := ConditionContext{PrincipalArn: "arn:aws:iam::123:role/admin"}
	cases := []struct {
		op   string
		spec string
		want bool
	}{
		{"ArnEquals", "arn:aws:iam::123:role/admin", true},
		{"ArnEquals", "arn:aws:iam::123:role/other", false},
		{"ArnNotEquals", "arn:aws:iam::123:role/other", true},
		{"ArnNotEquals", "arn:aws:iam::123:role/admin", false},
		{"ArnLike", "arn:aws:iam::*:role/admin", true},
		{"ArnLike", "arn:aws:iam::123:role/*", true},
		{"ArnLike", "arn:aws:iam::999:role/*", false},
		{"ArnNotLike", "arn:aws:iam::999:role/*", true},
		{"ArnNotLike", "arn:aws:iam::*:role/admin", false},
	}
	for _, tc := range cases {
		got := EvaluateCondition(cond(tc.op, "aws:PrincipalArn", tc.spec), ctx)
		if got != tc.want {
			t.Errorf("%s spec=%s = %v, want %v", tc.op, tc.spec, got, tc.want)
		}
	}
}

// TestIAMConditions_IfExists covers the IfExists short-circuit: when the
// key is absent from context, the operator vacuously matches.
func TestIAMConditions_IfExists(t *testing.T) {
	ctx := ConditionContext{} // key absent
	// Without IfExists — missing key default-denies.
	if EvaluateCondition(cond("StringEquals", "custom:key", "value"), ctx) {
		t.Error("missing key without IfExists should return false")
	}
	// With IfExists — missing key vacuously matches.
	if !EvaluateCondition(cond("StringEqualsIfExists", "custom:key", "value"), ctx) {
		t.Error("missing key with IfExists should return true")
	}
	// With IfExists but key present — normal semantics apply.
	ctxPresent := ConditionContext{Extras: map[string][]string{"custom:key": {"other"}}}
	if EvaluateCondition(cond("StringEqualsIfExists", "custom:key", "value"), ctxPresent) {
		t.Error("IfExists with present key should evaluate normally (mismatch → false)")
	}
	if !EvaluateCondition(cond("StringEqualsIfExists", "custom:key", "other"), ctxPresent) {
		t.Error("IfExists with present key should evaluate normally (match → true)")
	}
}

// TestIAMConditions_Quantifiers covers ForAllValues and ForAnyValue
// against multi-valued context keys.
func TestIAMConditions_Quantifiers(t *testing.T) {
	ctx := ConditionContext{Extras: map[string][]string{
		"aws:TagKeys": {"env", "owner", "cost-center"},
	}}
	// ForAllValues: every context value must be in the spec allowlist.
	condAllHit := cond("ForAllValues:StringEquals", "aws:TagKeys", "env")
	// Replace the scalar spec with a multi-value allowlist.
	condAllHit["ForAllValues:StringEquals"].(map[string]any)["aws:TagKeys"] = []any{"env", "owner", "cost-center", "extra"}
	if !EvaluateCondition(condAllHit, ctx) {
		t.Error("ForAllValues with full allowlist should match")
	}
	condAllMiss := cond("ForAllValues:StringEquals", "aws:TagKeys", "env")
	condAllMiss["ForAllValues:StringEquals"].(map[string]any)["aws:TagKeys"] = []any{"env", "owner"}
	if EvaluateCondition(condAllMiss, ctx) {
		t.Error("ForAllValues with partial allowlist should NOT match (cost-center missing)")
	}
	// ForAnyValue: at least one context value must be in spec.
	condAnyHit := cond("ForAnyValue:StringEquals", "aws:TagKeys", "env")
	if !EvaluateCondition(condAnyHit, ctx) {
		t.Error("ForAnyValue with matching spec should match")
	}
	condAnyMiss := cond("ForAnyValue:StringEquals", "aws:TagKeys", "department")
	if EvaluateCondition(condAnyMiss, ctx) {
		t.Error("ForAnyValue with no matching spec should NOT match")
	}
	// ForAllValues with empty context key → vacuously true (PMapper footgun).
	emptyCtx := ConditionContext{Extras: map[string][]string{"aws:TagKeys": {}}}
	if !EvaluateCondition(condAllMiss, emptyCtx) {
		t.Error("ForAllValues with empty context key should be vacuously true")
	}
}

// TestIAMConditions_UnknownOperator covers the conservative fallback: an
// unknown operator returns false.
func TestIAMConditions_UnknownOperator(t *testing.T) {
	ctx := ConditionContext{PrincipalArn: "arn:aws:iam::123:user/alice"}
	if EvaluateCondition(cond("BogusOperator", "aws:PrincipalArn", "x"), ctx) {
		t.Error("unknown operator should return false")
	}
}

// TestIAMConditions_MultiOperator covers an AND across multiple operator
// blocks within a single Condition map.
func TestIAMConditions_MultiOperator(t *testing.T) {
	ctx := ConditionContext{
		PrincipalArn: "arn:aws:iam::123:user/alice",
		MFAPresent:   true,
	}
	both := map[string]any{
		"StringEquals": map[string]any{
			"aws:PrincipalArn": "arn:aws:iam::123:user/alice",
		},
		"Bool": map[string]any{
			"aws:MultiFactorAuthPresent": "true",
		},
	}
	if !EvaluateCondition(both, ctx) {
		t.Error("all operator blocks match → true")
	}
	// Flip MFA off → one block fails → whole thing fails.
	ctx.MFAPresent = false
	if EvaluateCondition(both, ctx) {
		t.Error("one failing operator block should fail the whole condition")
	}
}

// TestIAMConditions_EmptyCondition covers the vacuous-match path — an
// empty or nil Condition block always matches.
func TestIAMConditions_EmptyCondition(t *testing.T) {
	ctx := ConditionContext{}
	if !EvaluateCondition(nil, ctx) {
		t.Error("nil condition should vacuously match")
	}
	if !EvaluateCondition(map[string]any{}, ctx) {
		t.Error("empty condition should vacuously match")
	}
}

// cond builds a single-operator, single-key condition map for brevity.
// The leaf value is scalar; tests that need multi-value specs overwrite
// the leaf directly (see TestIAMConditions_Quantifiers).
func cond(op, key, value string) map[string]any {
	return map[string]any{
		op: map[string]any{
			key: value,
		},
	}
}
