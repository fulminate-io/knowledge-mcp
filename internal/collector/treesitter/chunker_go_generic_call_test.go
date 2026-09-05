// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// chunker_go_generic_call_test.go covers the generic-call arm of the Go Calls
// query, on a parse-only fixture. THE REAL-CORPUS KNOWN-POSITIVE THAT PAIRS
// WITH IT reads the server module's store package as its entire corpus and
// therefore lives in chunker_go_generic_call_server_test.go, which the sync
// script removes from the published tree; this file ships and runs in both
// layouts, because a fixture chunked in memory needs no tree at all.
//
// That query matched call_expression only, but the grammar parses an
// explicitly instantiated generic call — `newPresizedMap[string, int](100)` —
// as a type_conversion_expression wrapping a generic_type, so the site emitted
// no CALLS edge at all.

// goGenericCallSrc carries every conversion shape the grammar distinguishes,
// so the exact-set assertion below is a discriminating control rather than a
// spot check. Parse-only fixture: it is chunked, never compiled.
const goGenericCallSrc = `package generics

type Pair[A any, B any] struct {
	a A
	b B
}

type Conv []byte

type T struct{}

func newPresizedMap[K comparable, V any](n int) map[K]V {
	return make(map[K]V, n)
}

func genericCaller(s string, m map[string]int, x int, pr Pair[int, int], p *T, arr []func(int), y int) {
	_ = newPresizedMap[string, int](100)
	_ = errs.AsType[*MyErr](x)
	_ = Conv(s)
	_ = []byte(s)
	_ = map[string]int(m)
	_ = [4]byte(s)
	_ = interface{}(x)
	_ = chan int(x)
	_ = (*T)(p)
	_ = (*Pair[int, int])(p)
	arr[0](y)
	_ = Pair[int, int](pr)
}
`

// TestGoGenericCallCallees pins the EXACT callee set the fixture emits. An
// exact set is what makes the conversion controls real: a leak from any
// conversion shape changes the set and fails, whereas a NotContains list would
// have to guess the leaked spelling in advance.
//
// The set's four members, and why each is what it is:
//
//   - `Conv(s)` is a GENUINE type conversion that emits a CALLS edge TODAY and
//     still does after the fix: the Go grammar reads a bare-identifier
//     conversion as a call_expression, indistinguishable from a call.
//     Pre-existing, untouched, and NOT a regression introduced here.
//
//   - `[]byte(s)`, `map[string]int(m)`, `[4]byte(s)`, `interface{}(x)` and
//     `chan int(x)` are the DISCRIMINATING CONTROL: each parses as a
//     type_conversion_expression whose type child is a slice_type, map_type,
//     array_type, interface_type or channel_type, none of which the new arm
//     names, so each stays edge-free before AND after.
//
//   - `(*T)(p)`, `(*Pair[int, int])(p)` and `arr[0](y)` are the SECOND control
//     band, covering the shapes closest to the new arm. The first two are
//     call_expressions whose function is a parenthesized_expression, which
//     binds no @callee capture at all; the third is a call_expression whose
//     function is an index_expression, because an INTEGER subscript keeps the
//     grammar out of generic_type. Each stays edge-free before AND after.
//     `(*Pair[int, int])(p)` is the sharpest of the three: it contains a
//     generic type in the exact syntactic neighborhood of the new arm and
//     must still emit nothing.
//
//   - `Pair[int, int](pr)` is a generic TYPE conversion and emits `Pair` after
//     the fix. This is an irreducible false positive — tree-sitter has no type
//     information, so a generic type conversion parses IDENTICALLY to a generic
//     function call — and it is in the expected set deliberately, so the
//     limitation lives in the test rather than surfacing later as a surprise.
//     Note the contrast the fixture makes explicit: the PARENTHESIZED pointer
//     form of the same type emits nothing while the bare form emits — the
//     grammar, not the semantics, decides.
func TestGoGenericCallCallees(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "generics.go", []byte(goGenericCallSrc))
	require.NoError(t, err)

	var got []string
	for _, e := range filterEdges(result.Edges, EdgeCalls) {
		if !strings.HasSuffix(e.FromID, "genericCaller") {
			continue
		}
		got = append(got, e.ToID)
	}

	require.ElementsMatch(t, []string{"Conv", "Pair", "errs.AsType", "newPresizedMap"}, got)
}
