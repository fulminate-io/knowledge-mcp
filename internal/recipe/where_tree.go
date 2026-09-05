// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// where_tree.go — the recipe DSL's predicate: an ast-style JSON boolean tree,
// decoded strictly, replacing the string-expression predicate form.
//
// THE SHAPE IS MIRRORED FROM cmd/knowledge/internal/ast, NOT IMPORTED. That
// package's evaluator is typed on *sitter.Node — a CGO struct with unexported
// fields no other package can construct — so a recipe row can never reach it.
// What travels is the shape and the strict-decode discipline; the evaluator is
// this package's own, over sourceView rows.
//
// Cost: decoding happens once per recipe parse, and parsed recipes are AST-cached
// by run_recipe.go, so nothing here runs per row or per run.

// WhereNode is one node of the predicate tree: zero or more composers and zero
// or more leaves. Several set on one node are implicitly AND-ed, matching ast's
// evalWhere.
type WhereNode struct {
	All []*WhereNode `json:"all,omitempty"`
	Any []*WhereNode `json:"any,omitempty"`
	Not *WhereNode   `json:"not,omitempty"`

	Kind       *KindLeaf    `json:"kind,omitempty"`
	Matches    *MatchesLeaf `json:"matches,omitempty"`
	Equals     *EqualsLeaf  `json:"equals,omitempty"`
	Exists     *ExistsLeaf  `json:"exists,omitempty"`
	Compare    *CompareLeaf `json:"compare,omitempty"`
	Ancestor   *EdgeLeaf    `json:"ancestor,omitempty"`
	Descendant *EdgeLeaf    `json:"descendant,omitempty"`
}

// KindLeaf tests the TYPE of a row. Of names a ROW — a select type, a traverse
// alias, or `node` — rather than a field, which is the one place the `of`
// vocabulary differs across leaves. Is accepts one source node type or a list.
type KindLeaf struct {
	Of string
	Is []string
}

// MatchesLeaf tests a field path against a Go regexp.
//
// IT CARRIES NO COMPILED REGEX, AND THAT ABSENCE IS LOAD-BEARING. A parsed
// *Recipe is memoized in the process-global astCache and shared by every
// concurrent run of that recipe, so a compiled expression stored on the leaf
// would be an unsynchronized write into shared state — two sessions running one
// recipe raced on exactly that field, proven under -race, and a reader seeing it
// half-written as nil refused a valid recipe as a validator bug that never
// happened. The compiled expressions live in a PER-RUN map on Env instead, which
// keeps the cached tree read-only. See compileWhereTree and evalMatchesLeaf.
type MatchesLeaf struct {
	Of    string
	Regex string
}

// EqualsLeaf tests a field path for string equality.
type EqualsLeaf struct {
	Of    string
	Value string
}

// ExistsLeaf tests a field path for a non-empty value.
type ExistsLeaf struct {
	Of string
}

// CompareLeaf tests a field path NUMERICALLY against a literal operand: Of names
// the field, Op names one of the ordered operators compare_ops.go admits, and
// Value is the literal the field is compared against.
//
// IT CARRIES NO RESOLVED OPERATOR AND NO PARSED OPERAND, AND THAT ABSENCE IS THE
// POINT — the same absence MatchesLeaf states above, for the same reason. THE
// CACHED AST IS READ-ONLY: a parsed *Recipe is memoized in the process-global
// astCache and shared by every concurrent run of that recipe, and the daemon
// serializes nothing, so two sessions running one recipe reach these very leaf
// pointers. PER-RUN STATE LIVES ON Env, KEYED BY LEAF POINTER — here, in
// Env.whereCompares, written by the validator's resolve pass and read by
// evalCompareLeaf. The tell for the next feature that adds a leaf: a field on a
// cached type that the PARSER does not set is a field something later writes,
// and everything later runs per request.
type CompareLeaf struct {
	Of    string
	Op    string
	Value string
}

// EdgeLeaf tests whether a row has an ancestor (incoming walk) or a descendant
// (outgoing walk) along Edge that satisfies Where. Shared by both directions
// because the only difference is which way sourceView is walked.
type EdgeLeaf struct {
	Edge  string
	Where *WhereNode
}

// jsonStringOrArr unmarshals either a JSON string or an array of strings into a
// []string, so `"is":"section"` and `"is":["section","block"]` are the same
// thing. Mirrored from ast/where_json.go, where the same shape serves the same
// leaf.
//
// PLAIN json.Unmarshal IS CORRECT HERE, unlike everywhere else in this file: the
// payload is a string or a list of strings, which has no keys to typo, so there
// is no unknown-field class for strictness to catch.
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

// strictUnmarshal decodes with DisallowUnknownFields.
//
// IT IS APPLIED AT EVERY LEVEL, NOT ONLY THE OUTER ONE, and ast/where_json.go's
// own comment records why: with top-level strictness alone,
// {"kind":{"of":"node","is":"section","typo":1}} decodes cleanly and the typo
// vanishes, which is the silent-authoring class this whole ticket exists to
// close. Each leaf below therefore carries its own UnmarshalJSON routed through
// here.
func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// UnmarshalJSON decodes a kind leaf strictly.
func (l *KindLeaf) UnmarshalJSON(data []byte) error {
	var s struct {
		Of string          `json:"of"`
		Is jsonStringOrArr `json:"is"`
	}
	if err := strictUnmarshal(data, &s); err != nil {
		return err
	}
	l.Of, l.Is = s.Of, s.Is.values
	return nil
}

// UnmarshalJSON decodes a matches leaf strictly.
func (l *MatchesLeaf) UnmarshalJSON(data []byte) error {
	var s struct {
		Of    string `json:"of"`
		Regex string `json:"regex"`
	}
	if err := strictUnmarshal(data, &s); err != nil {
		return err
	}
	l.Of, l.Regex = s.Of, s.Regex
	return nil
}

// UnmarshalJSON decodes an equals leaf strictly.
func (l *EqualsLeaf) UnmarshalJSON(data []byte) error {
	var s struct {
		Of    string `json:"of"`
		Value string `json:"value"`
	}
	if err := strictUnmarshal(data, &s); err != nil {
		return err
	}
	l.Of, l.Value = s.Of, s.Value
	return nil
}

// UnmarshalJSON decodes an exists leaf strictly.
func (l *ExistsLeaf) UnmarshalJSON(data []byte) error {
	var s struct {
		Of string `json:"of"`
	}
	if err := strictUnmarshal(data, &s); err != nil {
		return err
	}
	l.Of = s.Of
	return nil
}

// UnmarshalJSON decodes a compare leaf strictly, so `{"of":…,"opp":…}` is
// refused rather than silently dropping the operator and comparing against a
// zero-value one.
func (l *CompareLeaf) UnmarshalJSON(data []byte) error {
	var s struct {
		Of    string `json:"of"`
		Op    string `json:"op"`
		Value string `json:"value"`
	}
	if err := strictUnmarshal(data, &s); err != nil {
		return err
	}
	l.Of, l.Op, l.Value = s.Of, s.Op, s.Value
	return nil
}

// UnmarshalJSON decodes an ancestor / descendant leaf strictly. Its nested
// `where` recurses through WhereNode's own strict decode.
func (l *EdgeLeaf) UnmarshalJSON(data []byte) error {
	var s struct {
		Edge  string     `json:"edge"`
		Where *WhereNode `json:"where"`
	}
	if err := strictUnmarshal(data, &s); err != nil {
		return err
	}
	l.Edge, l.Where = s.Edge, s.Where
	return nil
}

// compileWhereTree compiles every matches leaf in a tree, ONCE, before any row
// is evaluated.
//
// It is called by the validator, never by the evaluator, and it is what makes
// the evaluator's "no compile here" rule enforceable rather than aspirational.
// Each call into compileRegex bumps regexCompiles, so "compiled once per run" is
// a countable fact instead of a claim about where a call sits.
//
// IT WRITES INTO A PER-RUN MAP, NEVER INTO THE TREE. The tree it walks hangs off
// a *Recipe memoized in the process-global astCache and shared by every
// concurrent run of that recipe, so storing the compiled expression on the leaf
// made the cached AST mutable and raced. Keying the map on the leaf POINTER is
// what lets the compiled expression stay per-run while the leaf it belongs to
// stays shared and read-only: the pointers are stable for the life of the cached
// tree, and each run holds its own map.
//
// A bad pattern is refused naming the pattern and the leaf's field, so the
// author learns about it before the walk rather than on whichever row happens
// to reach the leaf first.
func compileWhereTree(w *WhereNode, pos Position, into map[*MatchesLeaf]*regexp.Regexp) error {
	if w == nil {
		return nil
	}
	for _, child := range w.All {
		if err := compileWhereTree(child, pos, into); err != nil {
			return err
		}
	}
	for _, child := range w.Any {
		if err := compileWhereTree(child, pos, into); err != nil {
			return err
		}
	}
	if err := compileWhereTree(w.Not, pos, into); err != nil {
		return err
	}
	for _, leaf := range []*EdgeLeaf{w.Ancestor, w.Descendant} {
		if leaf == nil {
			continue
		}
		if err := compileWhereTree(leaf.Where, pos, into); err != nil {
			return err
		}
	}
	if w.Matches == nil {
		return nil
	}
	re, err := compileRegex(w.Matches.Regex)
	if err != nil {
		return fmt.Errorf("matches leaf on %q at %d:%d: regex %q does not compile: %w",
			w.Matches.Of, pos.Line, pos.Col, w.Matches.Regex, err)
	}
	into[w.Matches] = re
	regexCompiles.Add(1)
	return nil
}

// whereTreeVocabulary is the accepted key set, rendered into every rejection so
// an author can repair the recipe from the message alone.
const whereTreeVocabulary = "Composers: all, any, not. " +
	"Leaves: kind{of,is}, matches{of,regex}, equals{of,value}, exists{of}, " +
	"compare{of,op,value}, ancestor{edge,where}, descendant{edge,where}."

// ParseWhereTree decodes a raw where-tree span into a *WhereNode, strictly.
//
// pos is the position of the span's OPENING BRACE, so the rejection reads
// line:col like every other recipe error and points at the tree rather than at
// wherever the JSON decoder happened to stop.
//
// THE REJECTION NAMES FIVE THINGS, in this order: the offending key (carried by
// the decoder's own error), the accepted vocabulary, that the run was refused
// BEFORE any row was read rather than walked with the key ignored, and where the
// full grammar lives. Each exists because an author reading only "invalid JSON"
// cannot tell a typo from an unsupported feature.
func ParseWhereTree(data []byte, pos Position) (*WhereNode, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil, fmt.Errorf(
			"parse error at %d:%d: empty where-tree — a where or filter clause must carry a predicate. %s See help(\"recipes\")",
			pos.Line, pos.Col, whereTreeVocabulary)
	}

	var w WhereNode
	if err := strictUnmarshal([]byte(trimmed), &w); err != nil {
		return nil, fmt.Errorf(
			"parse error at %d:%d: where-tree rejected: %w. %s "+
				"The run was refused before any row was read rather than walked with the key ignored. "+
				"See help(\"recipes\") for the where-tree grammar",
			pos.Line, pos.Col, err, whereTreeVocabulary)
	}
	return &w, nil
}
