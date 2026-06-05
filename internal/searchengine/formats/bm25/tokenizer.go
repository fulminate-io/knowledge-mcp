// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokenize splits text into searchable tokens with frequency counts. Ported
// VERBATIM from cmd/knowledge-server/internal/index/bm25/tokenizer.go (the
// authoritative server algorithm) so the client-built BM25 segments produce
// byte-identical token+frequency maps to what the server indexes. The only
// change is the store.ToLowerASCII dependency, which is inlined as the local
// toLowerASCII (the engine subpackage stays import-clean: stdlib + own subpkgs).
//
// Rules:
//   - Split on whitespace and punctuation (/, ., (, ), {, }, etc.)
//   - Split camelCase: getUserByID → [get, user, by, id, getuserbyid]
//   - Split snake_case: get_user_by_id → [get, user, by, id]
//   - Lowercase all tokens
//   - Remove tokens shorter than 2 characters
//   - Keep original compound token (lowercased) for exact-match boosting
func tokenize(text string) map[string]int {
	tokens := make(map[string]int)

	// Fast path: pre-lowercase entire text once, then slice at word boundaries.
	// Eliminates per-word toLowerASCII allocations for ASCII text.
	if isASCII(text) {
		lowerText := toLowerASCII(text)
		splitOnDelimitersFuncOffset(text, func(start, end int) {
			if end-start < 2 {
				return
			}
			word := text[start:end]
			lower := lowerText[start:end]
			tokens[lower]++
			tokenizeWord(word, lower, tokens)
		})
		return tokens
	}

	// Slow path: per-word lowering for non-ASCII text.
	splitOnDelimitersFunc(text, func(word string) {
		if len(word) < 2 {
			return
		}
		lower := toLowerASCII(word)
		tokens[lower]++
		tokenizeWord(word, lower, tokens)
	})
	return tokens
}

// tokenizeWord extracts sub-tokens from a word (snake_case or camelCase splitting).
// For camelCase, it slices the pre-computed `lower` at the same byte offsets as
// the original word's camelCase boundaries, avoiding per-part toLowerASCII allocs.
func tokenizeWord(word, lower string, tokens map[string]int) {
	if strings.Contains(word, "_") {
		tokenizeSnakeCase(word, lower, tokens)
		return
	}
	// Inline camelCase split using lower for zero-alloc part extraction.
	// Since lower is the ASCII-lowered version of word, byte offsets are 1:1.
	tokenizeCamelParts(word, lower, tokens)
}

// tokenizeCamelParts splits word on camelCase boundaries and indexes parts
// by slicing lower at the same offsets — zero allocation per part.
func tokenizeCamelParts(word, lower string, tokens map[string]int) {
	start := 0
	n := len(word)
	for i := 1; i < n; i++ {
		prev, cur := word[i-1], word[i]
		split := isLowerASCII(prev) && isUpperASCII(cur)
		if !split && i+1 < n {
			split = isUpperASCII(prev) && isUpperASCII(cur) && isLowerASCII(word[i+1])
		}
		if !split {
			continue
		}
		emitCamelPart(lower, start, i, tokens)
		start = i
	}
	emitCamelPart(lower, start, n, tokens)
}

func emitCamelPart(lower string, start, end int, tokens map[string]int) {
	if end-start < 2 {
		return
	}
	pl := lower[start:end]
	if pl != lower {
		tokens[pl]++
	}
}

// tokenizeSnakeCase splits a snake_case identifier and indexes all sub-tokens.
// lower is the pre-lowered version of word for zero-alloc camelCase sub-splitting.
func tokenizeSnakeCase(word, lower string, tokens map[string]int) {
	off := 0
	for part := range strings.SplitSeq(word, "_") {
		partLen := len(part)
		if partLen >= 2 {
			partLower := lower[off : off+partLen]
			tokens[partLower]++
			tokenizeCamelParts(part, partLower, tokens)
		}
		off += partLen + 1 // +1 for the "_" separator
	}
}

// splitOnDelimitersFuncOffset calls fn with the start/end byte offsets of each
// word token in text. ASCII-only — callers must verify isASCII before calling.
func splitOnDelimitersFuncOffset(text string, fn func(start, end int)) {
	wordStart := -1
	for i := 0; i < len(text); i++ {
		b := text[i]
		isWord := (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || b == '_'
		if isWord && wordStart < 0 {
			wordStart = i
		} else if !isWord && wordStart >= 0 {
			fn(wordStart, i)
			wordStart = -1
		}
	}
	if wordStart >= 0 {
		fn(wordStart, len(text))
	}
}

// splitOnDelimitersFunc calls fn for each token in text, splitting on whitespace
// and code punctuation. For ASCII-only text it uses a byte-level span scanner to
// avoid the rune-decoding overhead of strings.FieldsFunc. Non-ASCII text falls
// back to a rune-aware streaming split so Unicode letters are handled correctly.
func splitOnDelimitersFunc(text string, fn func(string)) {
	if isASCII(text) {
		// Fast path: byte-level span scanner.
		start := -1
		for i := 0; i < len(text); i++ {
			b := text[i]
			isWordByte := (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
				(b >= '0' && b <= '9') || b == '_'
			if isWordByte && start < 0 {
				start = i
				continue
			}
			if !isWordByte && start >= 0 {
				fn(text[start:i])
				start = -1
			}
		}
		if start >= 0 {
			fn(text[start:])
		}
		return
	}

	// Slow path: rune-aware streaming split. Decodes runes via utf8 so non-ASCII
	// word characters (é, 漢, etc.) aren't treated as delimiters. Streams
	// substrings directly to fn — no intermediate []string.
	start := -1
	pos := 0
	for pos < len(text) {
		r, size := utf8.DecodeRuneInString(text[pos:])
		isWordRune := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
		if isWordRune && start < 0 {
			start = pos
		} else if !isWordRune && start >= 0 {
			fn(text[start:pos])
			start = -1
		}
		pos += size
	}
	if start >= 0 {
		fn(text[start:])
	}
}

// isASCII reports whether all bytes in s are in the ASCII range.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// isUpperASCII reports whether b is an ASCII uppercase letter.
func isUpperASCII(b byte) bool { return b >= 'A' && b <= 'Z' }

// isLowerASCII reports whether b is an ASCII lowercase letter.
func isLowerASCII(b byte) bool { return b >= 'a' && b <= 'z' }
