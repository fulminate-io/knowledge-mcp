// SPDX-License-Identifier: Apache-2.0

// where_json.go — JSON parse helpers for the where-tree. Split from
// where.go to keep both files under the 500-LOC fail threshold (rule
// 710d9f91). Contains the polymorphic-string-or-array unmarshaler used by
// KindLeaf and the public ParseWhere entry point.

package ast

import (
	"encoding/json"
	"fmt"
	"strings"
)

// jsonStringOrArr unmarshals either a JSON string or an array of strings
// into a []string. Used by KindLeaf so callers can write `is: "kind"` or
// `is: ["k1", "k2"]` interchangeably.
type jsonStringOrArr struct {
	values []string
}

// UnmarshalJSON implements the encoding/json.Unmarshaler shape.
func (j *jsonStringOrArr) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		j.values = []string{s}
		return nil
	}
	return json.Unmarshal(data, &j.values)
}

// jsonUnmarshal decodes JSON with DisallowUnknownFields so leaf decoders
// (KindLeaf, MatchesLeaf, etc.) reject typo'd inner keys the same way the
// outer ParseWhere rejects typo'd composer/leaf keys. Without this,
// {kind:{of:X,is:Y,extra_field_typo:Z}} would parse cleanly and the typo
// would silently disappear.
func jsonUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ParseWhere parses a JSON where-tree payload into a *WhereNode. Returns
// (nil, nil) for empty input so callers can pass-through "no where filter"
// without special-casing the empty payload.
//
// Strict mode: unknown JSON keys at the WhereNode level are rejected
// (DisallowUnknownFields). The Phase 3 sweep found that typo'd shapes
// like {"of": "X", "is": "function_declaration"} (missing the outer
// "kind" wrapper) parsed cleanly into an empty WhereNode and silently
// returned all matches as if no filter were applied. The strict-mode
// rejection catches that authoring class with an actionable error
// instead of producing an unfiltered match set.
//
// Stable public surface — cmd/knowledge's MCP intercept calls this in
// Phase B'.4.
func ParseWhere(data []byte) (*WhereNode, error) {
	if len(data) == 0 {
		return nil, nil
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var w WhereNode
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("ast/where: parse where-tree: %w (valid keys: all, any, not, kind, matches, equals, same_node, same_text, inside_pattern, contains_pattern; leaf keys: kind={of,is}, matches={of,regex}, equals={of,value}, same_node={captures}, same_text={captures}, inside_pattern={of,pattern,where,as}, contains_pattern={of,pattern,where,as})", err)
	}
	return &w, nil
}
