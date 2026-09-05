// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// shrinkRunes are the letters whose simple lowercasing produces FEWER UTF-8
// bytes than the original. Slicing a pre-lowered buffer at offsets walked on
// the original word runs past its end for these — the panic this class covers.
//
// THIS LIST IS A CENSUS MEASURED ON UNICODE 17 (the tables Go 1.27 carries),
// kept as the documented record of the class — it is NOT the test's answer key
// and it is NOT the iteration set. The letter repertoire is a property of the
// TOOLCHAIN, not of the tokenizer: Go 1.26 carries Unicode 15, where U+A7CB
// (LATIN CAPITAL LETTER RAMS HORN) and U+A7DC (LATIN CAPITAL LETTER LAMBDA
// WITH STROKE) are unassigned, so unicode.IsLetter is false for them, the word
// splitter treats them as separators, and tokenize emits nothing. Driving a
// transcribed list through tokenize therefore fails on one toolchain and
// passes on another for a reason that has nothing to do with the code under
// test. driftingRunes below derives the real population at run time; this list
// is asserted as a SUBSET of it, restricted to the members the RUNNING
// toolchain agrees are letters.
var shrinkRunes = []rune{
	0x0130, 0x1E9E, 0x2126, 0x212B, 0x212A, 0x2C62, 0x2C64, 0x2C6D,
	0x2C6E, 0x2C6F, 0x2C70, 0x2C7E, 0x2C7F, 0xA78D, 0xA7AA, 0xA7AB,
	0xA7AC, 0xA7AD, 0xA7AE, 0xA7B0, 0xA7B1, 0xA7B2, 0xA7C5, 0xA7CB,
	0xA7DC,
}

// growRunes are the letters whose simple lowercasing produces MORE UTF-8 bytes
// than the original. These do not panic: the drifted offset lands short of the
// buffer's end and emits a spurious EXTRA token, which is why the assertion
// below is whole-map equality rather than a containment check. Same
// Unicode-17-census standing as shrinkRunes above.
var growRunes = []rune{0x023A, 0x023E}

// minDriftingCensus is the floor the runtime sweep must clear. It is the
// Unicode 15 size of the class (25 on Go 1.26), not the Unicode 17 size (27),
// because the floor has to hold on the OLDEST toolchain this tree builds under
// while still failing loudly if the sweep or the splitter stops finding the
// class at all. A newer toolchain that adds letters raises the count above the
// floor, which is the direction that is safe to be lenient in.
const minDriftingCensus = 25

// driftingRunes runs the exhaustive sweep this class is DEFINED by: every rune
// the RUNNING toolchain calls a letter whose simple lowercasing changes the
// UTF-8 byte length. Ascending code-point order, so subtest names and failure
// output are stable across runs.
//
// SWEPT AT TEST TIME RATHER THAN TRANSCRIBED, and that is the whole point: the
// population is a function of the toolchain's Unicode tables, so a transcribed
// set measures the toolchain's Unicode version instead of the tokenizer.
func driftingRunes() []rune {
	var out []rune
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !unicode.IsLetter(r) {
			continue
		}
		if len(string(r)) != len(strings.ToLower(string(r))) {
			out = append(out, r)
		}
	}
	return out
}

// TestTokenizeDriftingRuneClass drives every letter whose simple lowercasing
// changes UTF-8 byte length through tokenize. The population is driftingRunes'
// exhaustive sweep over the RUNNING toolchain's letters, not a list of the
// members that happened to be found once.
//
// The expectation is EXTERNAL — it comes from strings.ToLower, never from the
// tokenizer's own output — so the test cannot supply its own answer key.
//
// TWO GUARDS KEEP THE RUNTIME SWEEP FROM BECOMING ITS OWN ANSWER KEY, because a
// sweep that silently found nothing would otherwise pass vacuously. First, the
// recorded Unicode-17 census cross-checks it: every shrinkRunes/growRunes member
// the running toolchain still calls a letter MUST appear in the swept set, so a
// sweep that drops known members fails instead of shrinking. Second, the census
// floor: the class has at least minDriftingCensus letters on every toolchain
// this tree builds under. Every failure message names unicode.Version, because
// the one thing that legitimately moves these counts is the toolchain's tables.
func TestTokenizeDriftingRuneClass(t *testing.T) {
	drifting := driftingRunes()
	require.GreaterOrEqual(t, len(drifting), minDriftingCensus,
		"the exhaustive sweep found %d drifting letters on Unicode %s; the class has at least %d members on every supported toolchain, so a lower count means the sweep or the word splitter regressed, not that Unicode shrank",
		len(drifting), unicode.Version, minDriftingCensus)

	swept := make(map[rune]bool, len(drifting))
	for _, r := range drifting {
		swept[r] = true
	}
	for _, r := range append(append([]rune{}, shrinkRunes...), growRunes...) {
		if !unicode.IsLetter(r) {
			// Not a letter in THIS toolchain's tables (Unicode 15 lacks two of
			// the recorded census), so it is not in the class here and the
			// splitter never sees it as word material. See shrinkRunes.
			continue
		}
		require.True(t, swept[r],
			"U+%04X is a letter on Unicode %s and is in the recorded census, so the sweep must have found it; a member silently dropped must fail here, not shrink the assertion",
			r, unicode.Version)
	}

	for _, r := range drifting {
		t.Run(string(r), func(t *testing.T) {
			input := string(r) + "x"
			got := tokenize(input)
			require.Equal(t, map[string]int{strings.ToLower(input): 1}, got,
				"tokenize(%q) must emit exactly the lowered word (Unicode %s)", input, unicode.Version)
			for token := range got {
				require.True(t, utf8.ValidString(token),
					"tokenize(%q) emitted token %q cut mid-rune", input, token)
			}
		})
	}
}

// TestTokenizeLengthStableControls is the same-run known negative: non-ASCII
// text whose lowering does not drift, and ASCII camelCase, must tokenize
// IDENTICALLY after the fix. The values are copied verbatim from
// TestTokenizeExactMaps so a change in them is visible as a diff against a
// landed expectation.
func TestTokenizeLengthStableControls(t *testing.T) {
	require.Equal(t, map[string]int{"café": 2, "résumé": 1}, tokenize("café résumé café"))
	require.Equal(t, map[string]int{"漢字": 1}, tokenize("漢字"))
	require.Equal(t,
		map[string]int{"getuserbyid": 1, "get": 1, "user": 1, "by": 1, "id": 1},
		tokenize("getUserByID"))
}

// TestNewQueryTokensMatchBuildSide pins the query side to the build side on a
// drifting-rune input. NewQuery calls the same tokenize, so this asserts one
// answer for both rather than testing NewQuery against itself.
func TestNewQueryTokensMatchBuildSide(t *testing.T) {
	const input = "İstanbul"
	require.Equal(t, tokenize(input), NewQuery(input).tokens,
		"the query tokenizer must not diverge from the build tokenizer")
}

// asciiCorpus is camelCase and snake_case ASCII identifier text — the shape
// whose per-part lowering allocations the whole-text pre-lowering removed. It
// must carry uppercase-bearing camel parts: a corpus of already-lowercase words
// allocates nothing on either implementation and could not separate them.
const asciiCorpus = "getUserByID parseHTTPResponse core/domains/audit/service.go " +
	"get_user_by_id newSegmentBuilder ReplaceBucketGroup tokenizeDocsParallel " +
	"http_request_handler MarshalJSON unmarshalProtoMessage build_segment_index"

// BenchmarkTokenizeASCII measures tokenize's allocation cost per call on ASCII
// identifier text — the property the guarded fix must keep.
func BenchmarkTokenizeASCII(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		tokenize(asciiCorpus)
	}
}
