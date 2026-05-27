package text

import (
	"errors"
	"fmt"
)

// tokKind enumerates the PDF content-stream lexical token classes per
// PDF 32000-1:2008 §7.2 + §9.4.
type tokKind int

const (
	// tokEOF is returned when the tokenizer reaches the end of src.
	tokEOF tokKind = iota
	// tokOperator is an alphabetic operator name (e.g. Tj, BT, q).
	tokOperator
	// tokInt is a signed integer literal (no decimal point).
	tokInt
	// tokFloat is a signed real-number literal (decimal point present).
	tokFloat
	// tokName is a name object (slash-prefixed identifier); payload
	// excludes the leading "/".
	tokName
	// tokString is a literal string (paren-delimited). payload bytes
	// have escape sequences decoded; literal parens may appear inside
	// when balanced.
	tokString
	// tokHexString is a hex-string (angle-bracket delimited). payload
	// bytes are the decoded hex bytes (whitespace ignored, odd-digit
	// padded with trailing zero per §7.3.4.3).
	tokHexString
	// tokArrayStart corresponds to the literal "[" delimiter.
	tokArrayStart
	// tokArrayEnd corresponds to the literal "]" delimiter.
	tokArrayEnd
	// tokDictStart corresponds to the literal "<<" delimiter.
	tokDictStart
	// tokDictEnd corresponds to the literal ">>" delimiter.
	tokDictEnd
)

// token is a single lexed token. payload's ownership depends on
// escaped: when escaped == false, payload is a sub-slice of src
// (zero-alloc fast path; callers must not retain past tokenizer
// reuse); when escaped == true, payload is a freshly allocated
// []byte owned by the token and safe to retain.
type token struct {
	kind    tokKind
	payload []byte
	escaped bool
}

// tokenizer is a byte-stream lexer for PDF content-stream operators,
// operands, strings, names, and delimiters. Single-pass, allocation-
// free for unescaped payloads; allocations occur only when escape
// decoding is required.
type tokenizer struct {
	src []byte
	pos int
}

// newTokenizer constructs a tokenizer over src. src is borrowed, not
// copied; the caller must keep it valid for the lifetime of the
// tokenizer. Tokens with escaped == false alias slices of src.
func newTokenizer(src []byte) *tokenizer {
	return &tokenizer{src: src, pos: 0}
}

// next advances past whitespace and comments and returns the next
// token. Returns tokEOF (with payload = nil) at end of src.
func (t *tokenizer) next() (token, error) {
	t.skipTrivia()
	if t.pos >= len(t.src) {
		return token{kind: tokEOF}, nil
	}

	c := t.src[t.pos]
	switch {
	case c == '[':
		t.pos++
		return token{kind: tokArrayStart}, nil
	case c == ']':
		t.pos++
		return token{kind: tokArrayEnd}, nil
	case c == '<':
		// "<<" dict-start vs "<...>" hex-string. Peek next byte.
		if t.pos+1 < len(t.src) && t.src[t.pos+1] == '<' {
			t.pos += 2
			return token{kind: tokDictStart}, nil
		}
		return t.scanHexString()
	case c == '>':
		if t.pos+1 < len(t.src) && t.src[t.pos+1] == '>' {
			t.pos += 2
			return token{kind: tokDictEnd}, nil
		}
		return token{}, fmt.Errorf("pdf/text tokenizer: stray %q at offset %d", c, t.pos)
	case c == '(':
		return t.scanLiteralString()
	case c == '/':
		return t.scanName()
	case c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9'):
		return t.scanNumber()
	}
	if isRegular(c) {
		return t.scanOperator()
	}
	return token{}, fmt.Errorf("pdf/text tokenizer: unexpected byte %q at offset %d", c, t.pos)
}

// isWhitespace reports whether b is one of PDF 32000-1:2008 Table 1's
// six white-space characters.
func isWhitespace(b byte) bool {
	switch b {
	case 0x00, 0x09, 0x0A, 0x0C, 0x0D, 0x20:
		return true
	}
	return false
}

// isDelimiter reports whether b is one of PDF Table 2's delimiter
// characters: ()<>[]{}/%
func isDelimiter(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// isRegular reports whether b is a "regular character" — not
// whitespace and not a delimiter. Operators, name bodies, and number
// digits are composed from regular characters.
func isRegular(b byte) bool {
	return !isWhitespace(b) && !isDelimiter(b)
}

// skipTrivia advances past whitespace and `%` line comments. Comments
// extend to end-of-line (LF or CR).
func (t *tokenizer) skipTrivia() {
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		if isWhitespace(c) {
			t.pos++
			continue
		}
		if c == '%' {
			// Skip to end of line.
			for t.pos < len(t.src) && t.src[t.pos] != '\n' && t.src[t.pos] != '\r' {
				t.pos++
			}
			continue
		}
		return
	}
}

// scanOperator reads a sequence of regular characters as an operator
// name. Sub-slice fast path (no allocation).
func (t *tokenizer) scanOperator() (token, error) {
	start := t.pos
	for t.pos < len(t.src) && isRegular(t.src[t.pos]) {
		t.pos++
	}
	return token{kind: tokOperator, payload: t.src[start:t.pos]}, nil
}

// scanNumber reads a signed integer or real-number literal. Decision
// is made by `.` presence; PDF does not support exponent notation in
// content streams.
func (t *tokenizer) scanNumber() (token, error) {
	start := t.pos
	if t.src[t.pos] == '+' || t.src[t.pos] == '-' {
		t.pos++
	}
	hasDigit := false
	hasDot := false
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		if c >= '0' && c <= '9' {
			hasDigit = true
			t.pos++
			continue
		}
		if c == '.' && !hasDot {
			hasDot = true
			t.pos++
			continue
		}
		break
	}
	if !hasDigit {
		return token{}, fmt.Errorf("pdf/text tokenizer: malformed number at offset %d", start)
	}
	kind := tokInt
	if hasDot {
		kind = tokFloat
	}
	return token{kind: kind, payload: t.src[start:t.pos]}, nil
}

// scanName reads a /name. Spec §7.3.5: # introduces 2-hex-char
// escape inside the name body. Fast-path returns sub-slice; escape
// decoding allocates a new buffer.
func (t *tokenizer) scanName() (token, error) {
	t.pos++ // consume leading '/'
	start := t.pos
	hasEscape := false
	for t.pos < len(t.src) && isRegular(t.src[t.pos]) {
		if t.src[t.pos] == '#' {
			hasEscape = true
		}
		t.pos++
	}
	raw := t.src[start:t.pos]
	if !hasEscape {
		return token{kind: tokName, payload: raw}, nil
	}
	decoded, err := decodeNameEscapes(raw)
	if err != nil {
		return token{}, err
	}
	return token{kind: tokName, payload: decoded, escaped: true}, nil
}

// decodeNameEscapes decodes "#XX" hex sequences inside a name body.
// Returns a freshly allocated []byte.
func decodeNameEscapes(raw []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] != '#' {
			out = append(out, raw[i])
			continue
		}
		if i+2 >= len(raw) {
			return nil, errors.New("pdf/text tokenizer: truncated # escape in name")
		}
		hi := hexNibble(raw[i+1])
		lo := hexNibble(raw[i+2])
		if hi < 0 || lo < 0 {
			return nil, fmt.Errorf("pdf/text tokenizer: bad hex digits in name #%c%c", raw[i+1], raw[i+2])
		}
		out = append(out, byte(hi<<4|lo))
		i += 2
	}
	return out, nil
}

// hexNibble maps a hex digit byte to its 0-15 value, or -1 on error.
func hexNibble(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b - 'a' + 10)
	case b >= 'A' && b <= 'F':
		return int(b - 'A' + 10)
	}
	return -1
}
