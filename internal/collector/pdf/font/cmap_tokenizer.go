package font

import "strings"

// cMapTokenizer is the small CMap-grammar tokenizer.
//
// Recognized token kinds: keyword (bare ident), name (/foo), integer
// (decimal digits), hex string (<DEAD>), array delimiters ([ ]).
// Comments (`%` to end of line) are skipped during tokenization.
type cmapTokenizer struct {
	src []byte
	i   int
}

type cmapTokenKind int

const (
	tkKeyword cmapTokenKind = iota
	tkName
	tkInt
	tkHex
	tkArrayStart
	tkArrayEnd
)

type cmapToken struct {
	kind  cmapTokenKind
	text  string // keywords, names, ints
	bytes []byte // hex strings (decoded)
}

func newCMapTokenizer(src []byte) *cmapTokenizer {
	return &cmapTokenizer{src: src}
}

// next returns the next non-comment, non-whitespace token. Returns
// (zero, false) on EOF.
//
// PROGRESS GUARANTEE: every call to next() advances tk.i by at least
// one byte (or returns ok=false on EOF). Otherwise a malformed CMap
// containing an unrecognized delimiter character (e.g. a stray `>`,
// `(`, `)`) would loop forever because scanKeyword's loop terminates
// without advancing when the cursor is already on a delimiter.
func (tk *cmapTokenizer) next() (cmapToken, bool) {
	for {
		tk.skipWhitespaceAndComments()
		if tk.i >= len(tk.src) {
			return cmapToken{}, false
		}
		switch c := tk.src[tk.i]; {
		case c == '<':
			return tk.scanHex()
		case c == '/':
			return tk.scanName()
		case c == '[':
			tk.i++
			return cmapToken{kind: tkArrayStart}, true
		case c == ']':
			tk.i++
			return cmapToken{kind: tkArrayEnd}, true
		case c >= '0' && c <= '9', c == '-', c == '+':
			return tk.scanInt()
		case c == '(':
			// PostScript literal string — skip to matching ')',
			// honoring backslash escapes and nested parens (per
			// PostScript reference §3.2.2). CMaps use these in
			// /CIDSystemInfo (Adobe) (Identity) etc.
			tk.skipLiteralString()
			continue
		case c == '>':
			// Stray '>' (often the second char of '>>' dict close
			// after scanHex consumed the body). Advance and retry
			// to keep the tokenizer making progress.
			tk.i++
			continue
		case c == ')':
			// Stray ')' — same recovery as stray '>'.
			tk.i++
			continue
		default:
			t, ok := tk.scanKeyword()
			// Defensive: ensure scanKeyword advanced. If not, force
			// a 1-byte advance so the outer loop terminates.
			if ok && len(t.text) == 0 {
				tk.i++
				continue
			}
			return t, ok
		}
	}
}

// skipLiteralString consumes a PostScript (...) literal up to and
// including the closing ')'. Honors `\(` / `\)` escapes and nested
// parens. The literal's content is discarded; CMap callers don't
// reference it.
func (tk *cmapTokenizer) skipLiteralString() {
	tk.i++ // consume '('
	depth := 1
	for tk.i < len(tk.src) {
		c := tk.src[tk.i]
		switch c {
		case '\\':
			tk.i++
			if tk.i < len(tk.src) {
				tk.i++ // skip escaped char
			}
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				tk.i++
				return
			}
		}
		tk.i++
	}
}

func (tk *cmapTokenizer) skipWhitespaceAndComments() {
	for tk.i < len(tk.src) {
		c := tk.src[tk.i]
		switch c {
		case ' ', '\t', '\n', '\r':
			tk.i++
		case '%':
			for tk.i < len(tk.src) && tk.src[tk.i] != '\n' {
				tk.i++
			}
		default:
			return
		}
	}
}

// scanHex consumes <...> and returns the decoded bytes. Whitespace
// inside the hex is allowed (some CMaps split long tokens with newlines).
// The opening '<' is consumed; this routine bails early on premature
// EOF returning an empty token.
func (tk *cmapTokenizer) scanHex() (cmapToken, bool) {
	tk.i++ // consume '<'
	var hex strings.Builder
	for tk.i < len(tk.src) && tk.src[tk.i] != '>' {
		c := tk.src[tk.i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			tk.i++
			continue
		}
		hex.WriteByte(c)
		tk.i++
	}
	if tk.i < len(tk.src) {
		tk.i++ // consume '>'
	}
	bb := decodeHex(hex.String())
	return cmapToken{kind: tkHex, bytes: bb}, true
}

// decodeHex turns a hex string like "DEADBEEF" into bytes. Odd-length
// strings get a trailing '0' nibble (per Adobe convention). Skips
// non-hex characters silently.
func decodeHex(s string) []byte {
	var clean strings.Builder
	clean.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'A' && c <= 'F', c >= 'a' && c <= 'f':
			clean.WriteByte(c)
		}
	}
	cs := clean.String()
	if len(cs)%2 == 1 {
		cs += "0"
	}
	out := make([]byte, len(cs)/2)
	for i := range out {
		out[i] = hexNibble(cs[2*i])<<4 | hexNibble(cs[2*i+1])
	}
	return out
}

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return 0
}

// scanName consumes /foo and returns the name (without the slash).
func (tk *cmapTokenizer) scanName() (cmapToken, bool) {
	tk.i++ // consume '/'
	start := tk.i
	for tk.i < len(tk.src) && !isCMapDelim(tk.src[tk.i]) {
		tk.i++
	}
	return cmapToken{kind: tkName, text: string(tk.src[start:tk.i])}, true
}

// scanInt consumes a leading-digits-or-sign integer.
func (tk *cmapTokenizer) scanInt() (cmapToken, bool) {
	start := tk.i
	for tk.i < len(tk.src) && !isCMapDelim(tk.src[tk.i]) {
		tk.i++
	}
	return cmapToken{kind: tkInt, text: string(tk.src[start:tk.i])}, true
}

// scanKeyword consumes a bare identifier.
func (tk *cmapTokenizer) scanKeyword() (cmapToken, bool) {
	start := tk.i
	for tk.i < len(tk.src) && !isCMapDelim(tk.src[tk.i]) {
		tk.i++
	}
	return cmapToken{kind: tkKeyword, text: string(tk.src[start:tk.i])}, true
}

func isCMapDelim(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '<', '>', '[', ']', '/', '%':
		return true
	}
	return false
}
