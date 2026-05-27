// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_parser_conditions.go implements the PMapper-core AWS IAM condition
// operator set. A statement matches only when EvaluateCondition returns
// true for its Condition block. The evaluator is a pure function over a
// ConditionContext — no store access, no globals, no I/O.
//
// The on-the-wire shape of a condition block, per AWS grammar, is:
//
//	"Condition": {
//	  "<Operator>": { "<ConditionKey>": "<Value>" | ["v1","v2"] },
//	  ...
//	}
//
// Values may be a single string or a slice of strings. Operators may carry
// the "IfExists" suffix and/or the "ForAllValues:" / "ForAnyValue:" prefix.
// All operator names implemented by this file are drawn from the PMapper
// core set listed in the step description (25 base operators + 3 qualifiers).
//
// Files in the condition-evaluator family:
//
//   - iam_parser_conditions.go          — API, ConditionContext, dispatch, helpers
//   - iam_parser_conditions_string.go   — String* + Arn* operators (wildcards)
//   - iam_parser_conditions_numeric.go  — Bool + Numeric*
//   - iam_parser_conditions_date.go     — Date* + IpAddress/NotIpAddress
//
// Each sub-file exposes one evalXxx dispatcher that takes (key, values, ctx)
// and returns bool. The main dispatch table in dispatchOperator routes each
// base operator name to its evaluator.

import (
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

// ConditionContext is the request-side state used to evaluate IAM conditions.
// Standard aws: keys are surfaced as typed fields; custom keys and tag-style
// keys live in Extras. Absent keys (not in any typed field, not in Extras)
// trigger the "unknown context key" semantics — returns (nil, false) from
// contextValue, which short-circuits IfExists and default-denies otherwise.
type ConditionContext struct {
	PrincipalArn string
	Resource     string
	Now          time.Time
	MFAPresent   bool
	SourceIP     net.IP
	Extras       map[string][]string
}

// operatorModifier carries the parsed IfExists / ForAllValues / ForAnyValue
// flags extracted from an operator name. At most one quantifier may be set.
type operatorModifier struct {
	IfExists     bool
	ForAllValues bool
	ForAnyValue  bool
}

// EvaluateCondition returns true iff every operator block in cond matches
// the supplied context. An empty/nil condition block vacuously matches.
//
// The cond argument is the raw map[string]any lifted out of IAMStatement.Condition.
// Each inner value is expected to be map[string]any where the leaf is either
// a string or []any (of strings). Unknown or malformed shapes fall through
// as no-match (conservative).
func EvaluateCondition(cond map[string]any, ctx ConditionContext) bool {
	if len(cond) == 0 {
		return true
	}
	for rawOp, body := range cond {
		inner, ok := body.(map[string]any)
		if !ok {
			slog.Debug("topology/iam_parser: condition body not a map", "op", rawOp)
			return false
		}
		base, mod := parseOperator(rawOp)
		for key, v := range inner {
			values, ok := normalizeConditionValues(v)
			if !ok {
				slog.Debug("topology/iam_parser: condition values malformed", "op", rawOp, "key", key)
				return false
			}
			if !evalOperatorBlock(base, mod, key, values, ctx) {
				return false
			}
		}
	}
	return true
}

// normalizeConditionValues converts the raw JSON value (string, []any, or
// []string) into []string. Unknown shapes return (nil, false).
func normalizeConditionValues(v any) ([]string, bool) {
	switch t := v.(type) {
	case string:
		return []string{t}, true
	case []string:
		return t, true
	case []any:
		out := make([]string, 0, len(t))
		for _, elt := range t {
			s, ok := elt.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case bool:
		// Bool operator sometimes lands as a JSON bool rather than a string.
		if t {
			return []string{"true"}, true
		}
		return []string{"false"}, true
	case float64:
		// Numeric/Date operators sometimes decode as JSON numbers.
		return []string{formatFloat(t)}, true
	case nil:
		return nil, true
	}
	return nil, false
}

// parseOperator strips the IfExists suffix and ForAllValues / ForAnyValue
// prefix from op, returning the base operator name and the modifier flags.
func parseOperator(op string) (base string, mod operatorModifier) {
	base = op
	switch {
	case strings.HasPrefix(base, "ForAllValues:"):
		mod.ForAllValues = true
		base = strings.TrimPrefix(base, "ForAllValues:")
	case strings.HasPrefix(base, "ForAnyValue:"):
		mod.ForAnyValue = true
		base = strings.TrimPrefix(base, "ForAnyValue:")
	}
	if strings.HasSuffix(base, "IfExists") {
		mod.IfExists = true
		base = strings.TrimSuffix(base, "IfExists")
	}
	return base, mod
}

// evalOperatorBlock runs the operator on a single (key, values) pair,
// honoring the IfExists short-circuit and ForAllValues/ForAnyValue
// quantifier semantics against multi-valued context keys.
func evalOperatorBlock(base string, mod operatorModifier, key string, specValues []string, ctx ConditionContext) bool {
	ctxValues, present := contextValue(key, ctx)
	if !present {
		if mod.IfExists {
			return true
		}
		// Missing context keys default-deny (PMapper: "unknown context key").
		return false
	}
	// Apply quantifier semantics against multi-valued context keys. For
	// single-valued keys (the common case), both quantifiers collapse to
	// "does this one value match any spec value".
	switch {
	case mod.ForAllValues:
		// Every context value must match at least one spec value.
		// Empty context is vacuously true — a PMapper footgun documented
		// in AWS IAM policy grammar.
		for _, cv := range ctxValues {
			if !dispatchOperator(base, key, specValues, []string{cv}) {
				return false
			}
		}
		return true
	case mod.ForAnyValue:
		// At least one context value must match at least one spec value.
		for _, cv := range ctxValues {
			if dispatchOperator(base, key, specValues, []string{cv}) {
				return true
			}
		}
		return false
	default:
		return dispatchOperator(base, key, specValues, ctxValues)
	}
}

// dispatchOperator routes (base, key, specValues, ctxValues) to the
// right operator evaluator. ctxValues carries the already-resolved context
// values so evaluators do not re-query the context. Unknown operators
// log a warning and return false (conservative). The ConditionContext is
// intentionally not threaded through here — all typed context lookups
// happen in evalOperatorBlock via contextValue before dispatch, and every
// current operator evaluator is pure over (specValues, ctxValues).
func dispatchOperator(base, key string, specValues, ctxValues []string) bool {
	switch base {
	// String operators (iam_parser_conditions_string.go).
	case "StringEquals",
		"StringNotEquals",
		"StringEqualsIgnoreCase",
		"StringNotEqualsIgnoreCase",
		"StringLike",
		"StringNotLike":
		return evalStringOperator(base, specValues, ctxValues)
	// ARN operators (iam_parser_conditions_string.go).
	case "ArnEquals", "ArnLike", "ArnNotEquals", "ArnNotLike":
		return evalArnOperator(base, specValues, ctxValues)
	// Bool + Numeric operators (iam_parser_conditions_numeric.go).
	case "Bool":
		return evalBoolOperator(specValues, ctxValues)
	case "NumericEquals",
		"NumericNotEquals",
		"NumericLessThan",
		"NumericLessThanEquals",
		"NumericGreaterThan",
		"NumericGreaterThanEquals":
		return evalNumericOperator(base, specValues, ctxValues)
	// Date operators (iam_parser_conditions_date.go).
	case "DateEquals",
		"DateNotEquals",
		"DateLessThan",
		"DateLessThanEquals",
		"DateGreaterThan",
		"DateGreaterThanEquals":
		return evalDateOperator(base, specValues, ctxValues)
	// IP operators (iam_parser_conditions_date.go).
	case "IpAddress", "NotIpAddress":
		return evalIPOperator(base, specValues, ctxValues)
	}
	slog.Debug("topology/iam_parser: unknown condition operator", "operator", base, "key", key)
	return false
}

// contextValue resolves a condition key against the ConditionContext and
// returns its values plus a present flag. Standard aws: keys route to
// typed fields; everything else falls through to Extras (case-sensitive,
// as AWS IAM condition keys are).
func contextValue(key string, ctx ConditionContext) ([]string, bool) {
	switch key {
	case "aws:PrincipalArn":
		if ctx.PrincipalArn == "" {
			return nil, false
		}
		return []string{ctx.PrincipalArn}, true
	case "aws:SourceArn", "aws:SourceResource":
		if ctx.Resource == "" {
			return nil, false
		}
		return []string{ctx.Resource}, true
	case "aws:CurrentTime", "aws:EpochTime":
		if ctx.Now.IsZero() {
			return nil, false
		}
		return []string{ctx.Now.UTC().Format(time.RFC3339)}, true
	case "aws:MultiFactorAuthPresent":
		if ctx.MFAPresent {
			return []string{"true"}, true
		}
		return []string{"false"}, true
	case "aws:SourceIp":
		if len(ctx.SourceIP) == 0 {
			return nil, false
		}
		return []string{ctx.SourceIP.String()}, true
	}
	if ctx.Extras != nil {
		if v, ok := ctx.Extras[key]; ok {
			return v, true
		}
	}
	return nil, false
}

// formatFloat renders a JSON number back as its canonical string form.
// Integer-valued floats render without a trailing ".0" so downstream
// numeric parsing sees the same textual form the AWS policy would use.
func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
