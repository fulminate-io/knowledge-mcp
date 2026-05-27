package text

import (
	"errors"
	"fmt"
)

// scanLiteralString reads a paren-delimited literal string. Per PDF
// 32000-1:2008 §7.3.4.2, parentheses inside must balance, backslash
// introduces escape sequences (\\n, \\r, \\t, \\b, \\f, \\(, \\),
// \\\\, \\<EOL>, and octal \\ddd up to 3 digits). The fast path
// (no escapes, no nested parens beyond depth-1) returns a sub-slice;
// the escape path allocates.
func (t *tokenizer) scanLiteralString() (token, error) {
	if t.src[t.pos] != '(' {
		return token{}, fmt.Errorf("pdf/text tokenizer: scanLiteralString called at non-( offset %d", t.pos)
	}
	t.pos++ // consume '('
	start := t.pos
	depth := 1
	hasEscape := false
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		switch c {
		case '(':
			depth++
			t.pos++
		case ')':
			depth--
			if depth == 0 {
				raw := t.src[start:t.pos]
				t.pos++ // consume ')'
				if !hasEscape {
					return token{kind: tokString, payload: raw}, nil
				}
				dec, err := decodeLiteralStringEscapes(raw)
				if err != nil {
					return token{}, err
				}
				return token{kind: tokString, payload: dec, escaped: true}, nil
			}
			t.pos++
		case '\\':
			hasEscape = true
			t.pos += 2 // skip backslash + the escaped byte
			if t.pos > len(t.src) {
				return token{}, errors.New("pdf/text tokenizer: trailing \\ in literal string")
			}
		default:
			t.pos++
		}
	}
	return token{}, errors.New("pdf/text tokenizer: unterminated literal string")
}

// decodeLiteralStringEscapes decodes the backslash escape sequences
// defined in PDF 32000-1:2008 §7.3.4.2 Table 3. Allocates a fresh
// []byte.
func decodeLiteralStringEscapes(raw []byte) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			out = append(out, raw[i])
			continue
		}
		if i+1 >= len(raw) {
			return nil, errors.New("pdf/text tokenizer: trailing \\ in literal string")
		}
		c := raw[i+1]
		switch c {
		case 'n':
			out = append(out, '\n')
			i++
		case 'r':
			out = append(out, '\r')
			i++
		case 't':
			out = append(out, '\t')
			i++
		case 'b':
			out = append(out, '\b')
			i++
		case 'f':
			out = append(out, '\f')
			i++
		case '(', ')', '\\':
			out = append(out, c)
			i++
		case '\r':
			// Backslash-CR or backslash-CRLF -> elide line break.
			if i+2 < len(raw) && raw[i+2] == '\n' {
				i += 2
			} else {
				i++
			}
		case '\n':
			// Backslash-LF -> elide line break.
			i++
		case '0', '1', '2', '3', '4', '5', '6', '7':
			// Octal escape: 1-3 octal digits.
			val := 0
			n := 0
			for n < 3 && i+1+n < len(raw) {
				d := raw[i+1+n]
				if d < '0' || d > '7' {
					break
				}
				val = (val << 3) | int(d-'0')
				n++
			}
			out = append(out, byte(val&0xff))
			i += n
		default:
			// Reserved/unknown escape: drop the backslash, keep the
			// following byte (spec §7.3.4.2: "the reverse solidus
			// and the character following it shall be ignored").
			out = append(out, c)
			i++
		}
	}
	return out, nil
}

// scanHexString reads a hex-string ('<' ... '>') and returns the
// decoded bytes. Whitespace inside is ignored (§7.3.4.3); odd hex-
// digit count pads the trailing nibble with zero. Allocates a fresh
// buffer because the source has whitespace/case-insensitive hex
// digits that must be packed into bytes.
func (t *tokenizer) scanHexString() (token, error) {
	if t.src[t.pos] != '<' {
		return token{}, fmt.Errorf("pdf/text tokenizer: scanHexString called at non-< offset %d", t.pos)
	}
	t.pos++ // consume '<'
	out := make([]byte, 0, 16)
	have := 0 // 0 = expect high nibble, 1 = low nibble
	high := 0
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		if c == '>' {
			t.pos++
			if have == 1 {
				out = append(out, byte(high<<4))
			}
			return token{kind: tokHexString, payload: out, escaped: true}, nil
		}
		if isWhitespace(c) {
			t.pos++
			continue
		}
		n := hexNibble(c)
		if n < 0 {
			return token{}, fmt.Errorf("pdf/text tokenizer: bad hex digit %q at offset %d", c, t.pos)
		}
		if have == 0 {
			high = n
			have = 1
		} else {
			out = append(out, byte((high<<4)|n))
			have = 0
		}
		t.pos++
	}
	return token{}, errors.New("pdf/text tokenizer: unterminated hex string")
}
