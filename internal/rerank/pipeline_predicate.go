// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// rerank_pipeline_predicate.go owns Predicate.Eval and its supporting
// helpers: leaf field reader, $query interpolation, the 7 match operators,
// and the inline tokenizer. Split from rerank_pipeline.go solely to keep
// both files under the 300-line warn threshold; no new exports.
//
// Validate-time machinery (mutual-exclusion + depth + regex compile) lives
// in rerank_pipeline.go because it is part of the parse path.

// Eval evaluates the predicate against a single hydrated node, with the
// live search query string available for `$query` interpolation. Returns
// true if the result matches the predicate.
//
// Composition order:
//  1. Boolean composition (any/all/not) recurses first, with short-circuit.
//  2. Leaf form reads the field, interpolates $query, applies the match.
//  3. Negate is applied once at the end as XOR.
//
// Predicate.validate (called from per-op Validate, called from
// Pipeline.Validate, called from ParsePipeline) guarantees mutual
// exclusion + closed-set field/match enums + a precompiled regex when
// Match == "regex" — Eval relies on those invariants and does not
// re-check.
func (p *Predicate) Eval(query string, n *knowledgev1.Node) bool {
	var matched bool
	switch {
	case len(p.Any) > 0:
		matched = false
		for i := range p.Any {
			if p.Any[i].Eval(query, n) {
				matched = true
				break
			}
		}
	case len(p.All) > 0:
		matched = true
		for i := range p.All {
			if !p.All[i].Eval(query, n) {
				matched = false
				break
			}
		}
	case p.Not != nil:
		matched = !p.Not.Eval(query, n)
	default:
		matched = p.evalLeaf(query, n)
	}
	if p.Negate {
		return !matched
	}
	return matched
}

// evalLeaf reads the field from the node, decodes the JSON value with
// $query interpolation, and applies the match operator. Predicate
// invariants (closed-set Field, closed-set Match, eager-compiled regex)
// are guaranteed by validate; evalLeaf returns false on any unexpected
// shape rather than erroring (Eval has no error path — invalid
// predicates can't reach here).
func (p *Predicate) evalLeaf(query string, n *knowledgev1.Node) bool {
	fieldVal := readPredicateField(p.Field, n)
	switch p.Match {
	case "regex":
		// Eager-compiled in validate(); reject silently if missing
		// (defensive — should not happen given the invariant).
		if p.compiled == nil {
			return false
		}
		return p.compiled.MatchString(fieldVal)
	case "prefix":
		val, ok := decodeStringValue(p.Value, query)
		if !ok {
			return false
		}
		return strings.HasPrefix(fieldVal, val)
	case "suffix":
		val, ok := decodeStringValue(p.Value, query)
		if !ok {
			return false
		}
		return strings.HasSuffix(fieldVal, val)
	case "contains":
		val, ok := decodeStringValue(p.Value, query)
		if !ok {
			return false
		}
		return strings.Contains(fieldVal, val)
	case "equals":
		val, ok := decodeStringValue(p.Value, query)
		if !ok {
			return false
		}
		return fieldVal == val
	case "in":
		vals, ok := decodeStringSliceValue(p.Value, query)
		if !ok {
			return false
		}
		return slices.Contains(vals, fieldVal)
	case "tokens_match":
		val, ok := decodeStringValue(p.Value, query)
		if !ok {
			return false
		}
		return shareToken(fieldVal, val)
	}
	return false
}

// readPredicateField extracts the string value of the named field on n.
// The closed set matches the validator in rerank_pipeline.go. metadata.<key>
// resolves through kgtypes.Value (pkg/kgtypes/node_value.go:31).
func readPredicateField(field string, n *knowledgev1.Node) string {
	switch field {
	case "file_path":
		return n.FilePath
	case "symbol_name":
		return n.SymbolName
	case "type":
		return n.Type
	case "summary":
		return n.Summary
	case "description":
		return n.Description
	case "keywords":
		return n.Keywords
	case "signature":
		return n.Signature
	case "status":
		return n.Status
	case "content":
		return n.Content
	case "is_test":
		if n.IsTest {
			return "true"
		}
		return "false"
	case "test_kind":
		return n.TestKind
	}
	const prefix = "metadata."
	if strings.HasPrefix(field, prefix) {
		return kgtypes.Value(n, field[len(prefix):])
	}
	return ""
}

// decodeStringValue reads a JSON-encoded string Value and substitutes the
// live query for the literal "$query" token. The boolean signals whether
// the decode succeeded — callers should treat false as "predicate does
// not match" rather than panic.
func decodeStringValue(raw json.RawMessage, query string) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	if s == "$query" {
		return query, true
	}
	return s, true
}

// decodeStringSliceValue reads a JSON-encoded []string Value, substituting
// the live query for any element equal to the literal "$query" token.
func decodeStringSliceValue(raw json.RawMessage, query string) ([]string, bool) {
	var ss []string
	if err := json.Unmarshal(raw, &ss); err != nil {
		return nil, false
	}
	for i, s := range ss {
		if s == "$query" {
			ss[i] = query
		}
	}
	return ss, true
}

// shareToken reports whether two strings share at least one token after
// lowercasing and splitting on non-letter / non-number runes. Inline
// tokenizer (5 lines) — do NOT import from rankeval (test package).
func shareToken(a, b string) bool {
	at := tokenize(a)
	if len(at) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(at))
	for _, t := range at {
		seen[t] = struct{}{}
	}
	for _, t := range tokenize(b) {
		if _, ok := seen[t]; ok {
			return true
		}
	}
	return false
}

// tokenize lowercases s and splits on runes that are not letters or
// numbers. Empty input yields an empty slice.
func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}
