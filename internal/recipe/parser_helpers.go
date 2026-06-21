// SPDX-License-Identifier: Apache-2.0

package recipe

import "fmt"

// Consumer helpers — kept small and in one place so the rule parsers
// in parser.go stay focused on grammar.

func (p *parser) peek() Token {
	if p.pos >= len(p.tokens) {
		return Token{Kind: TokEOF}
	}
	return p.tokens[p.pos]
}

func (p *parser) consume() Token {
	t := p.peek()
	p.pos++
	return t
}

func (p *parser) matchIdent(name string) bool {
	t := p.peek()
	return t.Kind == TokIdent && t.Value == name
}

func (p *parser) expect(kind TokenKind, label string) error {
	t := p.peek()
	if t.Kind != kind {
		return p.errorf(t, "expected %s, got %q", label, t.Value)
	}
	p.consume()
	return nil
}

func (p *parser) expectIdent(label string) (Token, error) {
	t := p.peek()
	if t.Kind != TokIdent {
		return Token{}, p.errorf(t, "expected %s, got %q", label, t.Value)
	}
	p.consume()
	return t, nil
}

// skipNewlines consumes any number of TokNewline tokens.
func (p *parser) skipNewlines() {
	for p.peek().Kind == TokNewline {
		p.consume()
	}
}

// skipSeparators consumes newlines and commas — used between
// field-map entries where either is a valid separator.
func (p *parser) skipSeparators() {
	for p.peek().Kind == TokNewline || p.peek().Kind == TokComma {
		p.consume()
	}
}

func (p *parser) errorf(at Token, format string, args ...any) error {
	return &ParseError{Line: at.Pos.Line, Col: at.Pos.Col, Msg: fmt.Sprintf(format, args...)}
}
