// SPDX-License-Identifier: Apache-2.0

package recipe

import "fmt"

// lexArrow handles the three `-` cases: `--[rel]-->`, `-->`, or an
// error (a bare `-` is not a recipe token in v1).
func (l *lexer) lexArrow() (Token, error) {
	pos := l.pos()
	if l.peek(1) != '-' {
		return Token{}, l.errorf("expected '-->' or '--[rel]-->'; got '-' followed by %q", l.peek(1))
	}
	// Consume "--".
	l.advance()
	l.advance()
	if l.off < len(l.src) && l.src[l.off] == '[' {
		return l.lexRelArrow(pos)
	}
	if !l.matchAhead(">") {
		return Token{}, l.errorf("expected '-->' but got '--'")
	}
	l.advance() // '>'
	return Token{Kind: TokArrow, Value: "-->", Pos: pos}, nil
}

// lexRelArrow consumes the `[rel]-->` tail after the leading `--` has
// already been eaten. pos is the position of the first `-` so the
// emitted Token refers to the whole arrow.
func (l *lexer) lexRelArrow(pos Position) (Token, error) {
	l.advance() // consume '['
	start := l.off
	for l.off < len(l.src) && l.src[l.off] != ']' && l.src[l.off] != '\n' {
		l.advance()
	}
	if l.off >= len(l.src) || l.src[l.off] != ']' {
		return Token{}, l.errorf("unterminated --[rel]--> edge relation")
	}
	rel := string(l.src[start:l.off])
	l.advance() // consume ']'
	if !l.matchAhead("-->") {
		return Token{}, l.errorf("expected '-->' after '--[%s]'", rel)
	}
	l.advance()
	l.advance()
	l.advance()
	return Token{Kind: TokRelArrow, Value: rel, Pos: pos}, nil
}

// lexString consumes a "..."-delimited literal. Supported escape
// sequences: \" \\ \n \t \r. Anything else after a backslash is
// passed through literally — recipes that need raw bytes can write
// \x via two backslashes if the underlying byte ever needs to be a
// literal backslash.
func (l *lexer) lexString() (Token, error) {
	pos := l.pos()
	l.advance() // consume opening "
	var buf []byte
	for l.off < len(l.src) {
		ch := l.src[l.off]
		if ch == '\n' {
			return Token{}, l.errorf("unterminated string literal")
		}
		if ch == '"' {
			l.advance()
			return Token{Kind: TokString, Value: string(buf), Pos: pos}, nil
		}
		if ch == '\\' {
			esc := l.peek(1)
			if esc == 0 {
				return Token{}, l.errorf("trailing backslash in string literal")
			}
			l.advance()
			switch esc {
			case 'n':
				buf = append(buf, '\n')
			case 't':
				buf = append(buf, '\t')
			case 'r':
				buf = append(buf, '\r')
			default:
				// `\"` and `\\` land here unchanged — the lexer treats
				// any other escape as "drop the backslash, keep the
				// next byte literally," matching the prior behavior so
				// existing recipes don't break.
				buf = append(buf, esc)
			}
			l.advance()
			continue
		}
		buf = append(buf, ch)
		l.advance()
	}
	return Token{}, l.errorf("unterminated string literal")
}

// lexVar consumes a `$name` token; name follows ident rules.
func (l *lexer) lexVar() (Token, error) {
	pos := l.pos()
	l.advance() // consume '$'
	if l.off >= len(l.src) || !isIdentStart(l.src[l.off]) {
		return Token{}, l.errorf("expected identifier after '$'")
	}
	start := l.off
	for l.off < len(l.src) && isIdentPart(l.src[l.off]) {
		l.advance()
	}
	name := string(l.src[start:l.off])
	return Token{Kind: TokVar, Value: name, Pos: pos}, nil
}

// lexRegex consumes a `/pattern/` literal. Supports `\/` escape.
func (l *lexer) lexRegex() (Token, error) {
	pos := l.pos()
	l.advance() // consume opening /
	var buf []byte
	for l.off < len(l.src) {
		ch := l.src[l.off]
		if ch == '\n' {
			return Token{}, l.errorf("unterminated regex literal")
		}
		if ch == '/' {
			l.advance()
			return Token{Kind: TokRegex, Value: string(buf), Pos: pos}, nil
		}
		if ch == '\\' && l.peek(1) == '/' {
			buf = append(buf, '/')
			l.advance()
			l.advance()
			continue
		}
		buf = append(buf, ch)
		l.advance()
	}
	return Token{}, l.errorf("unterminated regex literal")
}

// lexWhereJSON consumes the brace-balanced JSON where-tree span that follows
// `where` or `filter`, from the opening brace to its matching close, and emits
// it RAW — braces included, escapes untouched — for ParseWhereTree to hand to
// encoding/json.
//
// IT TRACKS STRING STATE, NOT JUST DEPTH. A brace inside a JSON string literal
// is data, not structure, so `{"matches":{"of":"node.name","regex":"^\\{"}}`
// must not close early; and a backslash inside a string escapes the next byte,
// so a pattern ending in `\"` must not be read as leaving the string.
//
// NEWLINES INSIDE THE SPAN ARE LEGAL and are consumed through l.advance(), so
// line:col stays accurate for every token after a multi-line tree.
//
// BOTH FAILURES CITE THE OPENING BRACE, never EOF. An author told only that the
// input ended has to find the span's start themselves; the position that lets
// them fix it is where the tree began.
func (l *lexer) lexWhereJSON() (Token, error) {
	openPos := l.pos()
	start := l.off
	depth := 0
	inString := false

	for l.off < len(l.src) {
		ch := l.src[l.off]
		switch {
		case inString && ch == '\\':
			// The escaped byte cannot end the string, whatever it is.
			l.advance()
			if l.off >= len(l.src) {
				return Token{}, &LexError{
					Line: openPos.Line, Col: openPos.Col,
					Msg: fmt.Sprintf("unterminated string inside the where-tree beginning at %d:%d", openPos.Line, openPos.Col),
				}
			}
			l.advance()
			continue
		case ch == '"':
			inString = !inString
		case inString:
			// Any other byte inside a string is data, including braces.
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				l.advance() // consume the closing brace
				return Token{Kind: TokWhereJSON, Value: string(l.src[start:l.off]), Pos: openPos}, nil
			}
		}
		l.advance()
	}

	if inString {
		return Token{}, &LexError{
			Line: openPos.Line, Col: openPos.Col,
			Msg: fmt.Sprintf("unterminated string inside the where-tree beginning at %d:%d", openPos.Line, openPos.Col),
		}
	}
	return Token{}, &LexError{
		Line: openPos.Line, Col: openPos.Col,
		Msg: fmt.Sprintf("unterminated where-tree: no matching '}' for the '{' at %d:%d", openPos.Line, openPos.Col),
	}
}

// lexIdent consumes an IDENT token. Kept tiny — one loop over
// isIdentPart. Cannot fail.
func (l *lexer) lexIdent() Token {
	pos := l.pos()
	start := l.off
	for l.off < len(l.src) && isIdentPart(l.src[l.off]) {
		l.advance()
	}
	return Token{Kind: TokIdent, Value: string(l.src[start:l.off]), Pos: pos}
}
