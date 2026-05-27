// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_parser_conditions_numeric.go holds the Bool and Numeric* condition
// operator evaluators. Bool coerces "true"/"false" (and JSON bool) to a
// real boolean; Numeric operators parse both spec and ctx as float64.
//
// Multi-valued spec: for equals-family operators we succeed if ANY spec
// value matches ANY ctx value. For not-equals, NO spec value may match
// any ctx value. Comparison operators (<, <=, >, >=) follow the same
// "any-hit" rule — this matches PMapper's conditions.py semantics.

import (
	"strconv"
	"strings"
)

// evalBoolOperator implements the Bool operator. AWS IAM canonicalizes
// values to the literal strings "true"/"false". We accept case-insensitive
// input for robustness against hand-written policies.
func evalBoolOperator(specValues, ctxValues []string) bool {
	for _, cv := range ctxValues {
		ctxBool, ok := parseIAMBool(cv)
		if !ok {
			return false
		}
		for _, sv := range specValues {
			specBool, ok := parseIAMBool(sv)
			if !ok {
				continue
			}
			if ctxBool == specBool {
				return true
			}
		}
	}
	return false
}

// parseIAMBool accepts "true"/"false" (any case) and returns the parsed
// bool plus an ok flag. Any other value yields (false, false).
func parseIAMBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// evalNumericOperator dispatches the 6 numeric comparison operators.
// Parse failures fall through as a false result (conservative).
func evalNumericOperator(op string, specValues, ctxValues []string) bool {
	for _, cv := range ctxValues {
		ctxNum, err := strconv.ParseFloat(strings.TrimSpace(cv), 64)
		if err != nil {
			return false
		}
		for _, sv := range specValues {
			specNum, err := strconv.ParseFloat(strings.TrimSpace(sv), 64)
			if err != nil {
				continue
			}
			if compareNumeric(op, ctxNum, specNum) {
				return true
			}
		}
	}
	return false
}

// compareNumeric returns true if ctxNum op specNum holds. Unknown op
// returns false (dispatchOperator only routes known names here).
func compareNumeric(op string, ctxNum, specNum float64) bool {
	switch op {
	case "NumericEquals":
		return ctxNum == specNum
	case "NumericNotEquals":
		return ctxNum != specNum
	case "NumericLessThan":
		return ctxNum < specNum
	case "NumericLessThanEquals":
		return ctxNum <= specNum
	case "NumericGreaterThan":
		return ctxNum > specNum
	case "NumericGreaterThanEquals":
		return ctxNum >= specNum
	}
	return false
}
