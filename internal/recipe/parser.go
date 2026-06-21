// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"strings"
)

// Consumer helpers (peek/consume/expect/…) live in parser_helpers.go.

// ParseError is the single error type returned by Parse. Carries the
// line:col of the offending token so recipe authors can jump directly
// to the mistake in their editor.
type ParseError struct {
	Line int
	Col  int
	Msg  string
}

// Error implements error.
func (e *ParseError) Error() string {
	return fmt.Sprintf("parse error at %d:%d: %s", e.Line, e.Col, e.Msg)
}

// Parse lexes src and builds a *Recipe AST. On any lex or parse error
// Parse returns a nil *Recipe and a non-nil *ParseError citing the
// offending token's line:col. Callers storing the recipe body must
// parse once at authoring time and log the error — never persist a
// recipe node whose Content fails Parse.
func Parse(src []byte) (*Recipe, error) {
	tokens, err := Lex(src)
	if err != nil {
		// Re-wrap lex errors as ParseErrors so callers need only one
		// error type — operator workflow is identical either way.
		if le, ok := err.(*LexError); ok {
			return nil, &ParseError{Line: le.Line, Col: le.Col, Msg: le.Msg}
		}
		return nil, err
	}
	p := &parser{tokens: tokens}
	return p.parseRecipe()
}

// parser holds the token cursor. Zero-indexed; tokens[len-1] is always
// TokEOF thanks to Lex's contract.
type parser struct {
	tokens []Token
	pos    int
}

// parseRecipe is the grammar entry. Drops leading newlines, parses
// rules until EOF, and wraps them in a Recipe.
func (p *parser) parseRecipe() (*Recipe, error) {
	p.skipNewlines()
	startPos := p.peek().Pos
	var rules []Rule
	for p.peek().Kind != TokEOF {
		rule, err := p.parseRule()
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
		p.skipNewlines()
	}
	return &Recipe{Rules: rules, Pos: startPos}, nil
}

// parseRule reads the keyword that starts a line and dispatches to the
// per-kind parser. Every rule MUST end at a newline or EOF — the per-
// rule parsers consume every token up to but not including the
// terminator.
func (p *parser) parseRule() (Rule, error) {
	tok := p.peek()
	if tok.Kind != TokIdent {
		return nil, p.errorf(tok, "expected rule keyword, got %q", tok.Value)
	}
	switch tok.Value {
	case "select":
		return p.parseSelect()
	case "traverse":
		return p.parseTraverse()
	case "filter":
		return p.parseFilter()
	case "bind":
		return p.parseBind()
	case "group_by":
		return p.parseGroupBy()
	case "emit":
		return p.parseEmit()
	case "lookup":
		return p.parseLookup()
	case "link":
		return p.parseLink()
	case "source_ref":
		return p.parseSourceRef()
	default:
		return nil, p.errorf(tok, "unknown rule keyword %q", tok.Value)
	}
}

// parseSelect parses `select IDENT [where expr]`.
func (p *parser) parseSelect() (Rule, error) {
	kw := p.consume() // 'select'
	nt, err := p.expectIdent("node type after 'select'")
	if err != nil {
		return nil, err
	}
	var where Expr
	if p.matchIdent("where") {
		p.consume()
		where, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
	}
	return RuleSelect{NodeType: nt.Value, Where: where, Pos: kw.Pos}, nil
}

// parseTraverse parses `traverse EDGE (in|out|both) [as VAR]`.
func (p *parser) parseTraverse() (Rule, error) {
	kw := p.consume() // 'traverse'
	edge, err := p.expectIdent("edge type after 'traverse'")
	if err != nil {
		return nil, err
	}
	dir, err := p.expectIdent("direction (in|out|both)")
	if err != nil {
		return nil, err
	}
	d := strings.ToLower(dir.Value)
	if d != "in" && d != "out" && d != "both" {
		return nil, p.errorf(dir, "traverse direction must be in|out|both, got %q", dir.Value)
	}
	var asName string
	if p.matchIdent("as") {
		p.consume()
		v := p.peek()
		if v.Kind != TokVar {
			return nil, p.errorf(v, "expected $var after 'as'")
		}
		p.consume()
		asName = v.Value
	}
	return RuleTraverse{EdgeType: edge.Value, Direction: d, As: asName, Pos: kw.Pos}, nil
}

// parseFilter parses `filter expr`.
func (p *parser) parseFilter() (Rule, error) {
	kw := p.consume()
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	return RuleFilter{Pred: e, Pos: kw.Pos}, nil
}

// parseBind parses `bind VAR := expr`.
func (p *parser) parseBind() (Rule, error) {
	kw := p.consume()
	v := p.peek()
	if v.Kind != TokVar {
		return nil, p.errorf(v, "expected $var after 'bind'")
	}
	p.consume()
	if err := p.expect(TokColonEq, "':='"); err != nil {
		return nil, err
	}
	val, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	return RuleBind{Var: v.Value, Value: val, Pos: kw.Pos}, nil
}

// parseGroupBy parses `group_by expr`.
func (p *parser) parseGroupBy() (Rule, error) {
	kw := p.consume()
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	return RuleGroupBy{Key: e, Pos: kw.Pos}, nil
}

// parseEmit parses `emit IDENT { field_map } [as VAR]`. Newlines
// inside the brace block are legal (and typical) — they delimit
// fields the same way commas do.
func (p *parser) parseEmit() (Rule, error) {
	kw := p.consume()
	nt, err := p.expectIdent("target node type after 'emit'")
	if err != nil {
		return nil, err
	}
	if err := p.expect(TokLBrace, "'{'"); err != nil {
		return nil, err
	}
	fields, err := p.parseFieldMap()
	if err != nil {
		return nil, err
	}
	if err := p.expect(TokRBrace, "'}'"); err != nil {
		return nil, err
	}
	var asName string
	if p.matchIdent("as") {
		p.consume()
		v := p.peek()
		if v.Kind != TokVar {
			return nil, p.errorf(v, "expected $var after 'as'")
		}
		p.consume()
		asName = v.Value
	}
	return RuleEmit{NodeType: nt.Value, Fields: fields, As: asName, Pos: kw.Pos}, nil
}

// parseLookup parses `lookup IDENT by expr as $VAR`. Identity is
// required (without it the rule has nothing to hash); so is the As
// binding (without it the lookup has no observable effect).
func (p *parser) parseLookup() (Rule, error) {
	kw := p.consume() // 'lookup'
	nt, err := p.expectIdent("target node type after 'lookup'")
	if err != nil {
		return nil, err
	}
	if !p.matchIdent("by") {
		return nil, p.errorf(p.peek(), "expected 'by' after lookup node type, got %q", p.peek().Value)
	}
	p.consume()
	identity, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if !p.matchIdent("as") {
		return nil, p.errorf(p.peek(), "expected 'as $var' after lookup identity")
	}
	p.consume()
	v := p.peek()
	if v.Kind != TokVar {
		return nil, p.errorf(v, "expected $var after 'as'")
	}
	p.consume()
	return RuleLookup{NodeType: nt.Value, Identity: identity, As: v.Value, Pos: kw.Pos}, nil
}

// parseLink parses `link expr --[rel]--> expr`. Bare `-->` without a
// relation is rejected; recipes must name the edge type.
func (p *parser) parseLink() (Rule, error) {
	kw := p.consume()
	from, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	rt := p.peek()
	if rt.Kind != TokRelArrow {
		return nil, p.errorf(rt, "expected '--[rel]-->' after link source")
	}
	p.consume()
	to, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	return RuleLink{From: from, Rel: rt.Value, To: to, Pos: kw.Pos}, nil
}

// parseSourceRef parses `source_ref expr`.
func (p *parser) parseSourceRef() (Rule, error) {
	kw := p.consume()
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	return RuleSourceRef{Ref: e, Pos: kw.Pos}, nil
}

// parseFieldMap parses `field_name := expr` entries separated by
// commas or newlines. Trailing commas are allowed.
func (p *parser) parseFieldMap() (map[string]Expr, error) {
	fields := map[string]Expr{}
	p.skipNewlines()
	for p.peek().Kind != TokRBrace && p.peek().Kind != TokEOF {
		name, err := p.expectIdent("field name")
		if err != nil {
			return nil, err
		}
		if err := p.expect(TokColonEq, "':='"); err != nil {
			return nil, err
		}
		val, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, exists := fields[name.Value]; exists {
			return nil, p.errorf(name, "duplicate field %q in emit block", name.Value)
		}
		fields[name.Value] = val
		p.skipSeparators()
	}
	return fields, nil
}
