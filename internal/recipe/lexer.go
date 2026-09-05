// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
)

// TokenKind enumerates the recipe-DSL token categories. The parser
// only cares about kind-level discrimination; source-exact spelling
// stays on Token.Value for error messages.
type TokenKind int

const (
	// TokEOF marks the end of input — always the last token in the
	// stream returned by Lex.
	TokEOF TokenKind = iota
	// TokNewline terminates a rule. The parser consumes it as a rule
	// separator; consecutive newlines are collapsed into one.
	TokNewline
	// TokIdent is a bareword — keyword (select, traverse, …) or
	// user-provided identifier (node type, edge type, field path
	// segment, …). Disambiguation is the parser's job.
	TokIdent
	// TokString is a double-quoted string literal. Value holds the
	// decoded content (backslash-escape handling lives in the lexer).
	TokString
	// TokVar is a `$name` variable reference. Value holds the name
	// without the leading dollar sign.
	TokVar
	// TokRegex is a `/pattern/` literal. Value holds the pattern
	// source (without surrounding slashes and with \/ escapes decoded).
	TokRegex
	// TokArrow is `-->` — used by RuleLink's simple form From --> To.
	TokArrow
	// TokRelArrow is `--[rel]-->` — edge-typed variant of TokArrow.
	// Value holds rel (without brackets).
	TokRelArrow
	// TokColonEq is `:=` — used by bind and emit-field assignment.
	TokColonEq
	// TokDot is `.` — the dotted-path separator in field refs.
	TokDot
	// TokComma is `,`.
	TokComma
	// TokLParen is `(`.
	TokLParen
	// TokRParen is `)`.
	TokRParen
	// TokLBrace is `{`.
	TokLBrace
	// TokRBrace is `}`.
	TokRBrace
	// TokTildeEq is `~=` — the regex-match operator.
	TokTildeEq
	// TokBangTilde is `!~` — the regex not-match operator.
	TokBangTilde
	// TokWhereJSON is the JSON where-tree span that follows `where` or
	// `filter`. Value holds the RAW, UNDECODED span INCLUDING the outer
	// braces.
	//
	// THAT IS THE OPPOSITE OF EVERY OTHER KIND IN THIS ENUM, and the
	// exception is deliberate. lexString strips its quotes and unescapes,
	// lexRegex strips its slashes and decodes \/, lexVar strips the sigil —
	// so the house rule is that a Token.Value is DECODED. A where-tree is
	// decoded by encoding/json in ParseWhereTree, which needs the braces and
	// the original escape sequences intact; a reader who applies the house
	// rule here and unescapes the span corrupts the JSON before the parser
	// ever sees it.
	TokWhereJSON
)

// Token is a lexed unit carrying its kind, source-exact spelling (or
// decoded value for STRING / REGEX / VAR), and Position for error
// reporting. The zero value is a TokEOF at line 0 — never surfaced by
// Lex directly, only by the parser's defensive look-ahead after EOF.
type Token struct {
	Kind  TokenKind
	Value string
	Pos   Position
}

// LexError is returned when the lexer encounters malformed source —
// unterminated strings, invalid escape sequences, unexpected bytes,
// etc. Message cites the Position so operators can navigate to the
// exact offset.
type LexError struct {
	Line int
	Col  int
	Msg  string
}

// Error implements the error interface.
func (e *LexError) Error() string {
	return fmt.Sprintf("lex error at %d:%d: %s", e.Line, e.Col, e.Msg)
}

// Lex scans src and returns the full token stream terminated by
// TokEOF. Comments (`# ... EOL`) are consumed and dropped; newlines
// that would otherwise separate two rules are preserved as
// TokNewline. Leading/trailing whitespace on a line is stripped so
// parsers downstream do not need to worry about indentation.
func Lex(src []byte) ([]Token, error) {
	lx := &lexer{src: src, line: 1, col: 1}
	var out []Token
	for {
		tok, err := lx.next()
		if err != nil {
			return nil, err
		}
		// ONE TOKEN OF LOOK-BEHIND, and it is the whole mechanism that lets a
		// JSON object ride a token stream that has no ':' token: an open brace
		// starts a where-tree span only when the token before it was the
		// identifier `where` or `filter`.
		lx.prev = tok
		out = append(out, tok)
		if tok.Kind == TokEOF {
			return out, nil
		}
	}
}

// lexer holds the scan cursor and line:col state. Not exported — the
// public surface is Lex and Token.
type lexer struct {
	src  []byte
	off  int
	line int
	col  int
	// prev is the last token Lex emitted. It exists for exactly one decision:
	// whether an open brace begins a where-tree span. Lex sets it after each
	// successful next(), so a lexer driven by calling next() directly (as some
	// tests do) sees a zero prev and never takes the span branch.
	prev Token
}

// next returns the next non-whitespace non-comment token. Whitespace
// (space / tab) inside a line is skipped but newlines emit TokNewline
// so the parser can treat line boundaries as rule terminators.
func (l *lexer) next() (Token, error) {
	l.skipInsignificant()
	if l.off >= len(l.src) {
		return Token{Kind: TokEOF, Pos: l.pos()}, nil
	}
	ch := l.src[l.off]
	// THE WHERE-TREE SPAN IS DECIDED BEFORE THE PUNCTUATION DISPATCH, because
	// '{' is otherwise a TokLBrace and an emit block would swallow the branch.
	// The predicate keys on the FOLLOWING BYTE as well as the preceding token:
	// nothing else in the grammar puts an open brace directly after `where` or
	// `filter` — parseEmit's brace follows the emit TYPE ident, and an emit
	// field literally named `where` is followed by ':='.
	if ch == '{' && l.prev.Kind == TokIdent && (l.prev.Value == "where" || l.prev.Value == "filter") {
		return l.lexWhereJSON()
	}
	if tok, ok := l.singleCharToken(ch); ok {
		return tok, nil
	}
	if tok, ok := l.twoCharToken(ch); ok {
		return tok, nil
	}
	switch {
	case ch == '-':
		return l.lexArrow()
	case ch == '"':
		return l.lexString()
	case ch == '$':
		return l.lexVar()
	case ch == '/':
		return l.lexRegex()
	case isIdentStart(ch):
		return l.lexIdent(), nil
	}
	return Token{}, l.errorf("unexpected byte %q", ch)
}

// singleCharToken returns the token for a single-byte punctuation
// character, or ok=false if ch is not a single-byte token.
func (l *lexer) singleCharToken(ch byte) (Token, bool) {
	kind, ok := singleCharKinds[ch]
	if !ok {
		return Token{}, false
	}
	pos := l.pos()
	l.advance()
	return Token{Kind: kind, Value: string(ch), Pos: pos}, true
}

// twoCharToken returns the token for a two-byte operator (:= / ~= / !~),
// or ok=false if ch does not start one.
func (l *lexer) twoCharToken(ch byte) (Token, bool) {
	var kind TokenKind
	var val string
	switch ch {
	case ':':
		if l.peek(1) != '=' {
			return Token{}, false
		}
		kind, val = TokColonEq, ":="
	case '~':
		if l.peek(1) != '=' {
			return Token{}, false
		}
		kind, val = TokTildeEq, "~="
	case '!':
		if l.peek(1) != '~' {
			return Token{}, false
		}
		kind, val = TokBangTilde, "!~"
	default:
		return Token{}, false
	}
	pos := l.pos()
	l.advance()
	l.advance()
	return Token{Kind: kind, Value: val, Pos: pos}, true
}

// singleCharKinds maps single-byte punctuation to token kinds.
var singleCharKinds = map[byte]TokenKind{
	'\n': TokNewline,
	'.':  TokDot,
	',':  TokComma,
	'(':  TokLParen,
	')':  TokRParen,
	'{':  TokLBrace,
	'}':  TokRBrace,
}

// skipInsignificant consumes spaces, tabs, and comments but leaves
// newlines for the caller so TokNewline can be emitted.
func (l *lexer) skipInsignificant() {
	for l.off < len(l.src) {
		ch := l.src[l.off]
		switch ch {
		case ' ', '\t', '\r':
			l.advance()
		case '#':
			for l.off < len(l.src) && l.src[l.off] != '\n' {
				l.advance()
			}
		default:
			return
		}
	}
}

// advance moves the scan cursor forward by one byte, maintaining
// line/col state so every Token.Position is accurate.
func (l *lexer) advance() {
	if l.off >= len(l.src) {
		return
	}
	if l.src[l.off] == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	l.off++
}

// peek returns the byte one position ahead, or 0 if past EOF. Used
// to disambiguate two-byte operators (`:=`, `~=`) from their single-
// byte prefixes.
func (l *lexer) peek(n int) byte { //nolint:unparam // n is always 1 today; kept as a parameter for readability at call sites and to allow >1 lookahead later without a signature churn
	if l.off+n >= len(l.src) {
		return 0
	}
	return l.src[l.off+n]
}

// matchAhead reports whether the bytes starting at the cursor equal s.
// Does not advance the cursor.
func (l *lexer) matchAhead(s string) bool {
	if l.off+len(s) > len(l.src) {
		return false
	}
	return string(l.src[l.off:l.off+len(s)]) == s
}

// pos snapshots the current line:col for the next token emission.
func (l *lexer) pos() Position { return Position{Line: l.line, Col: l.col} }

// errorf builds a LexError citing the current position.
func (l *lexer) errorf(format string, args ...any) error {
	return &LexError{Line: l.line, Col: l.col, Msg: fmt.Sprintf(format, args...)}
}

// isIdentStart reports whether ch can start an identifier (letter or
// underscore, ASCII only — the recipe DSL does not support Unicode
// idents in v1).
func isIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

// isIdentPart reports whether ch can continue an identifier (letter,
// digit, underscore, or hyphen — hyphens are common in node/edge type
// names like "translated-from").
func isIdentPart(ch byte) bool {
	return isIdentStart(ch) || (ch >= '0' && ch <= '9') || ch == '-'
}
