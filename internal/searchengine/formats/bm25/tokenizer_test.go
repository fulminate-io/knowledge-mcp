// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTokenizeExactMaps asserts the ported tokenize() produces byte-identical
// token+frequency maps to the authoritative server tokenizer across camelCase,
// snake_case, ASCII, and non-ASCII inputs. The expected maps are the EXACT output
// of cmd/knowledge-server/internal/index/bm25/tokenizer.go (verified against it):
// any drift in this port would change client-built BM25 segments relative to the
// server's index. Asserting whole-map equality (not just Contains) is the
// byte-identity gate the step criterion calls for.
func TestTokenizeExactMaps(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]int
	}{
		{
			name:  "camelCase",
			input: "getUserByID",
			// compound + camel parts; "id" is 2 chars so kept.
			want: map[string]int{"getuserbyid": 1, "get": 1, "user": 1, "by": 1, "id": 1},
		},
		{
			name:  "snake_case",
			input: "get_user_by_id",
			// compound kept; "by"/"id" are 2 chars kept; underscore parts split.
			want: map[string]int{"get_user_by_id": 1, "get": 1, "user": 1, "by": 1, "id": 1},
		},
		{
			name:  "ascii path",
			input: "core/domains/audit/service.go",
			want: map[string]int{
				"core": 1, "domains": 1, "audit": 1, "service": 1, "go": 1,
			},
		},
		{
			name:  "pascal acronym",
			input: "HTTPServer",
			want:  map[string]int{"httpserver": 1, "http": 1, "server": 1},
		},
		{
			name:  "numbers preserved",
			input: "sha256 http2 v2",
			want:  map[string]int{"sha256": 1, "http2": 1, "v2": 1},
		},
		{
			name:  "repeated token frequency",
			input: "user user user",
			want:  map[string]int{"user": 3},
		},
		{
			name:  "non-ascii letters",
			input: "café résumé café",
			// Non-ASCII word runes are kept whole (no camel split); café appears twice.
			want: map[string]int{"café": 2, "résumé": 1},
		},
		{
			name:  "non-ascii cjk mixed with camel",
			input: "parseJSON 漢字",
			want:  map[string]int{"parsejson": 1, "parse": 1, "json": 1, "漢字": 1},
		},
		{
			name:  "single short token dropped",
			input: "a bb",
			want:  map[string]int{"bb": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenize(tt.input)
			require.Equal(t, tt.want, got, "tokenize(%q) must match the server tokenizer byte-for-byte", tt.input)
		})
	}
}

// TestToLowerASCII covers the ported lowercase helper across the three branches:
// non-ASCII fallback, already-lower zero-alloc, and ASCII-with-upper conversion.
func TestToLowerASCII(t *testing.T) {
	require.Equal(t, "getuserbyid", toLowerASCII("getUserByID"))
	require.Equal(t, "alreadylower", toLowerASCII("alreadylower"))
	require.Equal(t, "café", toLowerASCII("Café"))
	require.Empty(t, toLowerASCII(""))
}
