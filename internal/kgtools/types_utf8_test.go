// SPDX-License-Identifier: Apache-2.0

// Package tools — TextResult / ErrorResult UTF-8 sanitization tests.
//
// The 2026-05-09 smoke run surfaced an `assemble()` no-args failure
// "graph server unavailable: graph server call: internal: marshal message:
// string field contains invalid UTF-8". Tool outputs that interpolate node
// content (Description, summaries, source) can pick up bytes from non-UTF-8
// source files in upstream collectors; protobuf string fields reject
// invalid UTF-8 hard, killing the response end-to-end. TextResult /
// ErrorResult now scrub at the boundary so a single bad byte somewhere in
// the graph doesn't tank the whole tool call.
package kgtools

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestTextResult_ReplacesInvalidUTF8(t *testing.T) {
	// Bare \xff is never valid in any UTF-8 sequence.
	bad := "before\xffafter"
	res := TextResult(bad)

	require := assert.New(t)
	require.Len(res.Content, 1)
	out := res.Content[0].Text
	require.True(utf8.ValidString(out), "TextResult must produce valid UTF-8")
	require.Contains(out, "before")
	require.Contains(out, "after")
	require.NotContains(out, "\xff")
	require.Contains(out, "�", "replacement character must be present")
}

func TestErrorResult_ReplacesInvalidUTF8(t *testing.T) {
	// 0xc3 0x28 is an invalid continuation byte sequence — \xc3 starts a
	// 2-byte rune but \x28 is ASCII '(' which can't be a continuation.
	bad := "msg with \xc3\x28 bad seq"
	res := ErrorResult(bad)

	require := assert.New(t)
	require.True(res.IsError)
	require.Len(res.Content, 1)
	out := res.Content[0].Text
	require.True(utf8.ValidString(out))
	require.True(strings.HasPrefix(out, "Error: "), "ErrorResult must keep the Error: prefix")
	require.Contains(out, "msg with")
	require.Contains(out, "bad seq")
}

func TestTextResult_ValidUTF8Untouched(t *testing.T) {
	// Hot path: unicode + emoji + multibyte sequences must round-trip exactly.
	in := "héllo 🎉 ʇsǝʇ プレフィックス"
	res := TextResult(in)
	assert.Equal(t, in, res.Content[0].Text)
}

func TestSanitizeUTF8_DirectHelper(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "valid_ascii", in: "hello", want: "hello"},
		{name: "valid_unicode", in: "héllo 🎉", want: "héllo 🎉"},
		{name: "single_bad_byte", in: "x\xffy", want: "x�y"},
		// strings.ToValidUTF8 collapses a run of invalid bytes into ONE
		// replacement char (it doesn't emit one per bad byte).
		{name: "consecutive_bad_bytes_collapse", in: "\xff\xfe\xfd", want: "�"},
		{name: "empty", in: "", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, sanitizeUTF8(c.in))
		})
	}
}
