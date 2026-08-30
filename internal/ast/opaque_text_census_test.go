// SPDX-License-Identifier: Apache-2.0

// opaque_text_census_test.go — the per-grammar span-gap census, and the
// classification that decides which measured kinds the matcher compares whole.
//
// THE QUESTION THIS ANSWERS. matchNode compares text only for a node with no
// children, so a node whose children do NOT cover its own byte span carries
// content nothing is ever compared against. Which grammars produce such a node,
// and for which kinds, is a MEASUREMENT — a hand list of "string-ish kinds" over
// 20 grammars is exactly what this package refuses everywhere else, and the
// answer is not the one a hand list would have written: Go's interpreted string
// is affected while its raw string is not, Python's strings are affected ONLY
// when they carry an escape sequence, and three grammars' COMMENT kinds are
// affected for the identical reason their string kinds are not.
//
// THE INSTRUMENT. For every node in a probe parse, sum the byte ranges its
// children cover and subtract them from the node's own range. A leftover run
// that is entirely whitespace is the inter-token layout the matcher is
// deliberately insensitive to; a leftover run carrying NON-WHITESPACE bytes is
// content no comparison can reach. That second set is the census.
//
// WHY A GAP IS NOT AUTOMATICALLY A DEFECT — the reason this file carries a
// classification table rather than just a list. A gap says only that bytes went
// uncompared, not what those bytes MEAN. Three benign shapes recur: a literal
// whose delimiters gap while a content child covers its value, a container whose
// element separator the grammar folds into the parent, and a line-continuation
// marker. None of those is a value a pattern author could have meant. What IS a
// defect is a gap holding the node's own content, and only those kinds are
// declared in LangConfig.OpaqueTextKinds. Every measured kind carries a verdict
// here; a kind with none FAILS, which is what stops a future grammar bump from
// quietly introducing a fourth wildcard.
//
// TWO HALVES, MIRRORING THE COMMENT CENSUS. This file is the hermetic half: one
// literal-rich snippet per registered grammar, parsed in-process, compared
// against a committed artifact on every run. opaque_text_corpus_test.go is the
// sampled half, which walks real repositories so the classification is not left
// resting on snippets an author chose.

package ast

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"

	"testing"
)

// opaqueCensusEnv selects the artifact write; unset means measure-and-compare so
// an ordinary suite run never dirties testdata. Mirrors AST_COMMENT_CENSUS_FILE.
const opaqueCensusEnv = "AST_OPAQUE_CENSUS_FILE"

// opaqueCensusName is the committed artifact, and the fixed path the
// self-verifying comparison always reads.
const opaqueCensusName = "opaque_text_census.txt"

// gapVerdict is the classification vocabulary. It is CLOSED: a measured kind
// must take one of these, and only verdictOpaque licenses a LangConfig
// declaration.
type gapVerdict string

const (
	// verdictOpaque — the gap holds the node's OWN CONTENT. These are the
	// defect: an inlined literal of such a kind constrained nothing. Every kind
	// carrying this verdict must appear in its language's OpaqueTextKinds, and
	// nothing else may.
	verdictOpaque gapVerdict = "opaque"
	// verdictDelimiter — the gap holds only the literal's delimiters or sigil
	// while a content child covers its value. The value already constrains; only
	// the delimiter SPELLING does not, so r"x" also matches r#"x"#.
	verdictDelimiter gapVerdict = "delimiter-only"
	// verdictSeparator — the gap holds an element or statement separator the
	// grammar folds into the parent instead of surfacing as a child.
	verdictSeparator gapVerdict = "separator-only"
	// verdictContinuation — the gap holds a line-continuation marker, which is
	// layout the author could not have meant as a value.
	verdictContinuation gapVerdict = "continuation-only"
	// verdictOperator — the gap holds an operator or type-punctuation token the
	// grammar folds into the parent node.
	verdictOperator gapVerdict = "operator-only"
)

// gapRow is one measured (language, kind) fact plus its classification.
type gapRow struct {
	lang    string
	kind    string
	verdict gapVerdict
	sample  string
}

func (r gapRow) line() string {
	return fmt.Sprintf("lang=%s kind=%s verdict=%s gap=%q", r.lang, r.kind, r.verdict, r.sample)
}

// gapSummary is the one-per-language roll-up. It exists so a grammar that gaps
// NOWHERE gets a recorded verdict of its own rather than being represented by an
// absence of rows: "this grammar covers every byte of its literals with children"
// is a measurement someone must be able to read off the artifact, and six of the
// twenty grammars are in exactly that state. An absence would be
// indistinguishable from a grammar nobody probed.
type gapSummary struct {
	lang  string
	kinds []string
}

func (s gapSummary) line() string {
	if len(s.kinds) == 0 {
		return fmt.Sprintf("lang=%s span_gap_kinds=none", s.lang)
	}
	return fmt.Sprintf("lang=%s span_gap_kinds=%s", s.lang, strings.Join(s.kinds, ","))
}

// TestOpaqueTextCensus measures every registered grammar, classifies every
// span-gap kind it finds, asserts the classification agrees with the live
// LangConfig registrations in BOTH directions, and compares the committed
// artifact against the fresh measurement.
func TestOpaqueTextCensus(t *testing.T) {
	require.Len(t, opaqueProbes, len(registeredLangs()),
		"every registered grammar needs a probe — an unprobed grammar is an unmeasured span-gap classification")

	measured := map[treesitter.Language]map[string]string{}
	var (
		rows      []gapRow
		summaries []gapSummary
	)
	for _, probe := range opaqueProbes {
		kinds := measureSpanGaps(t, probe)
		measured[probe.lang] = kinds
		summary := gapSummary{lang: string(probe.lang)}
		for kind := range kinds {
			if kind != errorGapKind {
				summary.kinds = append(summary.kinds, kind)
			}
		}
		sort.Strings(summary.kinds)
		summaries = append(summaries, summary)
		t.Logf("%s", summary.line())
		for kind, sample := range kinds {
			if kind == errorGapKind {
				continue
			}
			verdict, ok := gapVerdicts[probe.lang][kind]
			if !ok {
				t.Errorf("unclassified span-gap kind for %s: %q leaves %q uncompared.\n"+
					"  Classify it: if the gap holds the node's own CONTENT it is %q and must join this language's OpaqueTextKinds; "+
					"if it holds only a delimiter, a separator, a continuation marker or an operator, give it that verdict with the measured sample as the reason.",
					probe.lang, kind, sample, verdictOpaque)
				continue
			}
			rows = append(rows, gapRow{lang: string(probe.lang), kind: kind, verdict: verdict, sample: sample})
		}
	}

	assertOpaqueRegistrationsAgree(t, measured)
	compareOpaqueCensus(t, rows, summaries)
}

// assertOpaqueRegistrationsAgree pins the two directions that matter: every kind
// the table calls opaque is registered, and every registered kind is a kind the
// table calls opaque. Either direction alone permits a silent drift — a
// registration nothing measured, or a measured defect nothing fixed.
func assertOpaqueRegistrationsAgree(t *testing.T, measured map[treesitter.Language]map[string]string) {
	t.Helper()

	// The floor runs first and unconditionally: every assertion below ranges over
	// a set, and an empty set satisfies all of them. Six is under the seven
	// languages the census measured an opaque kind for, so a dropped registration
	// fails here rather than passing vacuously.
	declaring := 0
	for _, cfg := range registrySnapshot() {
		if len(cfg.OpaqueTextKinds) > 0 {
			declaring++
		}
	}
	require.GreaterOrEqualf(t, declaring, 6,
		"only %d languages declare OpaqueTextKinds, below the floor of six; an exhaustive check over an empty set proves nothing", declaring)

	for lang, cfg := range registrySnapshot() {
		// Both sides are collected as nil-when-empty rather than as an empty
		// slice, so the overwhelmingly common case — a grammar that declares
		// nothing because it needs nothing — compares equal instead of tripping on
		// nil-versus-empty and burying the real rows in noise.
		var want []string
		for kind, verdict := range gapVerdicts[lang] {
			if verdict == verdictOpaque {
				want = append(want, kind)
			}
		}
		sort.Strings(want)
		var got []string
		if len(cfg.OpaqueTextKinds) > 0 {
			got = append(got, cfg.OpaqueTextKinds...)
		}
		sort.Strings(got)
		assert.Equalf(t, want, got,
			"%s: the census classifies %v as opaque but LangConfig declares %v — the matcher must compare exactly the kinds the measurement found content-blind",
			lang, want, got)

		// A declared kind the hermetic probe never surfaced is a registration
		// resting on nothing. The probe is this file's own fixture, so this is a
		// gap in the probe or in the registration, and either way it must fail.
		for _, kind := range cfg.OpaqueTextKinds {
			_, seen := measured[lang][kind]
			assert.Truef(t, seen,
				"%s declares OpaqueTextKinds entry %q that its probe snippet never produced a span gap for — extend the probe to exercise it, or drop a registration nothing measured", lang, kind)
		}
	}
}

// measureSpanGaps parses every snippet in one grammar's probe set and returns
// the UNION of the node kinds carrying a non-whitespace child-span gap, each
// mapped to a bounded sample of the gap text.
func measureSpanGaps(t *testing.T, probe opaqueProbe) map[string]string {
	t.Helper()
	require.NotEmptyf(t, probe.srcs,
		"lang=%s has an empty probe set — coverage counts probes, so an empty one is an unmeasured grammar that still satisfies the count", probe.lang)

	out := map[string]string{}
	for i, src := range probe.srcs {
		tree, srcBytes, ok := parseClean(t, probe.lang, src)
		require.Truef(t, ok,
			"lang=%s probe snippet %d does not parse cleanly — a dirty parse measures the recovery tree, not the grammar", probe.lang, i)
		walkAllIncludingAnonymous(tree.RootNode(), func(n *sitter.Node) {
			gap, found := firstContentGap(n, srcBytes)
			if !found {
				return
			}
			if _, dup := out[n.Type()]; dup {
				return
			}
			out[n.Type()] = gap
		})
		tree.Close()
	}
	return out
}

// firstContentGap returns the first run of bytes inside n's span that no child
// covers and that is not entirely whitespace — the exact bytes the matcher's
// child-by-child comparison can never reach. A childless node has no gap by
// definition: matchNode compares it whole already.
func firstContentGap(n *sitter.Node, src []byte) (string, bool) {
	count := int(n.ChildCount())
	if count == 0 {
		return "", false
	}
	cursor := n.StartByte()
	take := func(a, b uint32) (string, bool) {
		if a >= b {
			return "", false
		}
		s := string(src[a:b])
		if strings.TrimFunc(s, unicode.IsSpace) == "" {
			return "", false
		}
		if len(s) > opaqueGapSampleCap {
			s = s[:opaqueGapSampleCap]
		}
		return s, true
	}
	for i := range count {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if s, hit := take(cursor, c.StartByte()); hit {
			return s, true
		}
		if c.EndByte() > cursor {
			cursor = c.EndByte()
		}
	}
	return take(cursor, n.EndByte())
}

// opaqueGapSampleCap bounds the recorded gap text so the artifact stays legible
// and so a long heredoc does not commit a page of someone else's source.
const opaqueGapSampleCap = 32

// compareOpaqueCensus fails unless the committed artifact matches the fresh
// measurement, and writes it when opaqueCensusEnv is set.
func compareOpaqueCensus(t *testing.T, rows []gapRow, summaries []gapSummary) {
	t.Helper()
	lines := make([]string, 0, len(rows)+len(summaries))
	for _, r := range rows {
		lines = append(lines, r.line())
	}
	for _, s := range summaries {
		lines = append(lines, s.line())
	}
	sort.Strings(lines)
	want := strings.Join(lines, "\n") + "\n"

	if name := os.Getenv(opaqueCensusEnv); name != "" {
		require.NoError(t, os.MkdirAll("testdata", 0o750))
		path := filepath.Join("testdata", filepath.Base(name))
		require.NoError(t, os.WriteFile(path, []byte(want), 0o600))
		t.Logf("census written: %s (%d rows)", path, len(lines))
		return
	}

	path := filepath.Join("testdata", opaqueCensusName)
	got, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
	require.NoError(t, err, "census artifact missing — regenerate with %s=%s", opaqueCensusEnv, opaqueCensusName)
	require.Equal(t, want, string(got),
		"census artifact is stale — regenerate with %s=%s go test ./cmd/knowledge/internal/ast/ -run TestOpaqueTextCensus", opaqueCensusEnv, opaqueCensusName)
}
