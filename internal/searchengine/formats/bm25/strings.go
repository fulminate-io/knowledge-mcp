// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"strings"
	"unsafe"
)

// toLowerASCII lowercases a string. For ASCII-only strings it avoids the
// Unicode overhead of strings.ToLower by converting in a byte copy. Falls
// back to strings.ToLower for strings containing non-ASCII bytes.
//
// Ported from cmd/knowledge-server/internal/store.ToLowerASCII so the client
// BM25 tokenizer produces byte-identical tokens without importing the server
// store package (the engine subpackage stays import-clean: stdlib + own subpkgs).
func toLowerASCII(s string) string {
	// Check if any uppercase ASCII bytes are present; if not, no work needed.
	hasUpper := false
	hasNonASCII := false
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b >= 0x80 {
			hasNonASCII = true
			break
		}
		if b >= 'A' && b <= 'Z' {
			hasUpper = true
		}
	}

	if hasNonASCII {
		return strings.ToLower(s)
	}
	if !hasUpper {
		return s // already lowercase, return original (zero allocation)
	}

	// ASCII with uppercase: convert byte-by-byte.
	buf := []byte(s)
	for i, b := range buf {
		if b >= 'A' && b <= 'Z' {
			buf[i] = b + 32
		}
	}
	return unsafe.String(&buf[0], len(buf)) //nolint:gosec // safe: buf is freshly allocated and not reused
}
