// SPDX-License-Identifier: Apache-2.0

// wrapper_census_test.go — the wrapper-context census: what each registered
// grammar can actually express.
//
// TestWrapperContextCensus runs one probe per (language, shape class) through
// EVERY registered wrapper and records which ones parse, what root the
// compiler would pick, and which ones HOST the shape. The vocabulary those
// rows are written in is checked next door, in wrapper_context_test.go.
//
// HOSTING, for the census, is whatever the COMPILER'S hosting test says — the
// census calls hostsPattern rather than keeping its own copy of the rule. A
// wrapper can parse a pattern without hosting it: the pattern's text becomes a
// fragment of some larger construct, and the smallest node covering it spills
// past what the caller wrote. Which wrappers a pattern compiles under is
// decided on exactly that property, so the census measures it directly rather
// than measuring "did it parse" — and by borrowing the predicate rather than
// restating it, the artifact cannot drift from the behavior it describes.
//
// HOSTED IS NOT THE SAME QUESTION AS "IS THIS SHAPE REACHABLE". A grammar can
// host a member-shaped pattern under its DECLARATION wrapper and still make
// class members unreachable, because the root kind it compiles to
// (C++: `declaration`) is not the kind real class members carry
// (`field_declaration`). That is why every row records the root kind beside
// the hosted flag — the kind column is where such a gap is visible, and a
// reader who greps only for hosted=none will miss it.
//
// THE ANCHOR DISCIPLINE is the corpus census's, deliberately not a second
// convention: every probe declares the hosted set measured TODAY, and a
// mismatch in EITHER direction fails. An unrecorded improvement is as red as
// a regression, and the flip is the reviewable record of the fix.
//
// PERF SHAPE: 21 languages x 7 shapes x at most 4 wrappers is well under 600
// parses of one-line snippets. Language cells run as parallel subtests with
// one parser each; no worker pool is nested inside a cell, because
// tree-sitter parsers and trees are not safe to share.

package ast

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// wrapperCensusEnv names the environment variable that selects the census
// filename. Unset means "measure and assert, write nothing", so an ordinary
// suite run never dirties testdata.
const wrapperCensusEnv = "AST_WRAPPER_CENSUS_FILE"

// The seven shape classes every grammar is probed against.
//
// shapeStmtKeyword exists because shapeStmt could not see the multi-context
// collapse. Every stmt probe is a bare CALL, and a bare call IS an expression
// in most grammars, so the class never exercised a form that is a statement and
// nothing else: Go's stmt row reads wrapper=decl parsed=no while
// `defer $X.Close()` compiles ERROR-free under that same decl wrapper. The
// keyword-led class is what makes a pattern hosted by several contexts at once
// visible in the artifact rather than only in an implementer's notes.
//
// shapeStmtBlock exists because shapeStmtKeyword could not see the member-keyword
// ambiguity the narrowing work is about. Every stmt_keyword probe for the
// JS family is `return $X`, and `class W { return x }` parses under no wrapper,
// so those rows already read hosted=no under the member wrapper and the member
// reading never appears. The class is only reachable from a keyword that is ALSO
// a legal method name, followed by a parameter-shaped parenthesis and a braced
// body — an `if`-with-braced-body. For javascript, typescript and tsx that shape
// is hosted by decl, stmt AND member: `if` reads as a method_definition inside a
// class body. Measured 2026-08-04, `while ($C) { $$$B }` carries the same
// two-variant ambiguity, while `for (...)`, `switch (...)` and `try {...}` do
// NOT — the parameter shape has to be legal too, which is why the probe set is
// not widened past the one keyword.
//
// THE JS-FAMILY stmt_block ROWS ARE THE PRE-NARROWING TRUTH AND ARE EXPECTED TO
// MOVE IN PHASE 6: this step records the ambiguity (hosted=decl,stmt,member),
// Phase 5's name-field narrowing removes the member variant, and Phase 6
// re-records the rows as hosted=decl,stmt. A future reader must not read that
// Phase 6 diff as a regression — the flip IS the fix, recorded in the anchor.
const (
	shapeWildcard    = "wildcard"
	shapeStmt        = "stmt"
	shapeStmtKeyword = "stmt_keyword"
	shapeStmtBlock   = "stmt_block"
	shapeMember      = "member"
	shapeDecl        = "decl"
	shapeExpr        = "expr"
)

// hostedNone is the summary value for a shape no registered wrapper hosts.
const hostedNone = "none"

// wrapperProbe is one (language, shape) measurement: the pattern to compile,
// or the reason the shape cannot be probed in this grammar, plus the anchor.
//
// wantHosted records the hosted set measured TODAY, as the comma-separated
// wrapper names in registration order, or hostedNone. It lives in code rather
// than being read back from the artifact because the artifact is a REGENERATED
// rendering: an expectation read from the thing it is meant to check can never
// fail. A behavior change is recorded by editing this anchor in the same
// commit as the engine change, so review sees the flip.
//
// unprobeable, when set, records the shape as hostedNone without touching a
// parser. It is for grammars that have no such construct at all — never for a
// shape that merely fails to compile, which is a measurement and must be
// measured.
type wrapperProbe struct {
	shape       string
	pattern     string
	unprobeable string
	wantHosted  string
}

// wrapperCell is one language's seven probes.
type wrapperCell struct {
	lang   treesitter.Language
	probes []wrapperProbe
}

// wrapperRow is one (language, shape, wrapper) fact line.
type wrapperRow struct {
	lang    string
	shape   string
	wrapper string
	context string
	parsed  bool
	root    string
	hosted  bool
	// reason is set ONLY when the member-keyword narrowing flipped this row's
	// hosted flag: the wrapper parses and raw-hosts the fragment, but the compile
	// drops the reading because its leading token is a keyword. Empty for every
	// other row, so it appears in the artifact only where the narrowing fired.
	reason string
}

func (r wrapperRow) line() string {
	root := r.root
	if root == "" {
		root = "-"
	}
	line := fmt.Sprintf("lang=%s shape=%s wrapper=%s context=%s parsed=%s root=%s hosted=%s",
		r.lang, r.shape, r.wrapper, r.context, yesNo(r.parsed), root, yesNo(r.hosted))
	if r.reason != "" {
		line += fmt.Sprintf(" reason=%q", r.reason)
	}
	return line
}

// narrowedCensusReason is the rejection reason a per-wrapper row carries when the
// member-keyword narrowing dropped its reading. It is a census-side echo of the
// engine's narrowedReason, kept short for the single-line artifact format.
const narrowedCensusReason = "member reading dropped by keyword narrowing; leading token is a keyword, not a member name"

// wrapperSummary is the one-per-(language, shape) roll-up line.
type wrapperSummary struct {
	lang   string
	shape  string
	hosted string
}

func (s wrapperSummary) line() string {
	return fmt.Sprintf("lang=%s shape=%s hosted=%s", s.lang, s.shape, s.hosted)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// TestWrapperContextCensus measures every registered grammar against the five
// shape classes and asserts each observed hosted set equals its anchor.
func TestWrapperContextCensus(t *testing.T) {
	configs := registeredConfigs(t)
	require.GreaterOrEqual(t, len(configs), registeredLangFloor,
		"the registry holds %d languages, below the floor of %d", len(configs), registeredLangFloor)
	require.Len(t, wrapperCensusCells, len(configs),
		"every registered language needs a census cell — an unprobed grammar is an unmeasured grammar")

	var (
		mu        sync.Mutex
		rows      []wrapperRow
		summaries []wrapperSummary
	)
	// Registered on the PARENT so both run after every parallel cell has
	// finished and the row set is complete.
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		reconcileWrapperRows(t, rows, summaries)
		writeWrapperCensus(t, rows, summaries)
	})

	for _, cell := range wrapperCensusCells {
		t.Run(string(cell.lang), func(t *testing.T) {
			t.Parallel()
			gotRows, gotSummaries := runWrapperCell(t, cell)
			mu.Lock()
			rows = append(rows, gotRows...)
			summaries = append(summaries, gotSummaries...)
			mu.Unlock()
		})
	}
}

// runWrapperCell evaluates one language's probes.
func runWrapperCell(t *testing.T, cell wrapperCell) ([]wrapperRow, []wrapperSummary) {
	t.Helper()
	cfg, ok := langConfigFor(cell.lang)
	require.Truef(t, ok, "census cell names %s, which has no registered LangConfig", cell.lang)

	parser := treesitter.NewParser()
	defer parser.Close()

	rows := make([]wrapperRow, 0, len(cell.probes)*len(cfg.Wrappers))
	summaries := make([]wrapperSummary, 0, len(cell.probes))
	for _, probe := range cell.probes {
		probeRows, hosted := evaluateWrapperProbe(t, parser, cell.lang, cfg, probe)
		rows = append(rows, probeRows...)
		summary := wrapperSummary{lang: string(cell.lang), shape: probe.shape, hosted: hosted}
		t.Logf("%s", summary.line())
		summaries = append(summaries, summary)
		assertHosted(t, cell.lang, probe, hosted)
	}
	return rows, summaries
}

// evaluateWrapperProbe runs one probe through every registered wrapper and
// returns its rows plus the hosted set.
func evaluateWrapperProbe(
	t *testing.T,
	parser *treesitter.Parser,
	lang treesitter.Language,
	cfg LangConfig,
	probe wrapperProbe,
) ([]wrapperRow, string) {
	t.Helper()
	if probe.unprobeable != "" {
		return nil, hostedNone
	}

	pat, err := Parse(probe.pattern)
	require.NoErrorf(t, err, "census probe %s/%s is not a valid DSL pattern", lang, probe.shape)
	subst, _ := substitutePlaceholders(pat, cfg)

	// The narrowing overlay: raw per-wrapper hosting pre-dates the member-keyword
	// narrowing, so a probe whose member reading the COMPILE drops (its leading
	// token is a keyword) would still read hosted=yes here. survivingContexts asks
	// the real compile which contexts a caller can actually reach; a raw-hosted
	// context missing from it was narrowed away, and the census records that as
	// unhosted with the reason rather than advertising a reading no caller can use.
	surv := survivingContexts(cfg, probe.pattern)

	rows := make([]wrapperRow, 0, len(cfg.Wrappers))
	hosts := make([]string, 0, len(cfg.Wrappers))
	for _, w := range cfg.Wrappers {
		row := wrapperRow{lang: string(lang), shape: probe.shape, wrapper: w.Name, context: w.Context}
		root, hosted := probeWrapper(parser, lang, w, subst)
		row.parsed = root != ""
		row.root = root
		row.hosted = hosted
		if hosted && surv != nil && !surv[w.Context] {
			row.hosted = false
			row.reason = narrowedCensusReason
			hosted = false
		}
		if hosted {
			hosts = append(hosts, w.Name)
		}
		rows = append(rows, row)
	}
	if len(hosts) == 0 {
		return rows, hostedNone
	}
	return rows, strings.Join(hosts, ",")
}

// survivingContexts returns the set of contexts a probe's pattern actually keeps
// after the member-keyword narrowing, or nil when the pattern does not compile at
// all (the raw hosting is then the whole story and no overlay applies). It runs
// the real union compile so the census reflects what a caller reaches rather than
// what a wrapper raw-hosts — the two diverge exactly where the narrowing fires.
func survivingContexts(cfg LangConfig, pattern string) map[string]bool {
	pat, err := Parse(pattern)
	if err != nil {
		return nil
	}
	kept, narrowed, err := compilePatternVariants(context.Background(), pat, cfg, "")
	if err != nil {
		return nil
	}
	defer closeVariants(kept)
	defer closeVariants(narrowed)
	out := make(map[string]bool, len(cfg.Wrappers))
	for _, v := range kept {
		for _, c := range v.Contexts {
			out[c] = true
		}
	}
	return out
}

// probeWrapper parses subst under one wrapper and asks the COMPILER'S OWN
// hosting test whether the wrapper hosts it. Returns the root kind (empty when
// the wrapper does not produce an ERROR-free parse) and the hosted flag.
//
// It calls hostsPattern rather than re-deriving the answer, and that is not a
// convenience: an independent copy of the rule measures a predicate the engine
// does not use. Span equality alone — which is all rule 1 is — reports every
// class member as unhosted, because tree-sitter keeps the member's trailing
// separator in the container's child list and rule 2 is what absorbs it. A
// census written against the narrower predicate would have shown a hosted=none
// row beside a wrapper that compiles the shape perfectly well.
//
// When the wrapper hosts, the reported root is the HOSTING root, which is the
// construct the pattern actually compiles to; when it does not, it is the
// smallest node covering the fragment, which is what shows a caller why not.
func probeWrapper(parser *treesitter.Parser, lang treesitter.Language, w ContextWrapper, subst string) (string, bool) {
	full := w.Prefix + subst + w.Suffix
	tree, err := parser.Parse(context.Background(), []byte(full), lang)
	if err != nil {
		return "", false
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		return "", false
	}

	userStart := uint32(len(w.Prefix))
	userEnd := userStart + uint32(len(subst))
	if h, ok := hostsPattern(root, userStart, userEnd); ok && h.root != nil {
		return h.root.Type(), true
	}
	effective, _ := smallestNodeCovering(root, userStart, userEnd, 0)
	if effective == nil {
		return "", false
	}
	return effective.Type(), false
}

// assertHosted compares one probe's observed hosted set against its anchor and
// fails on a mismatch in either direction, naming the direction and the edit
// that resolves it. Saying which way it moved is what stops the next reader
// from "fixing" a genuine improvement by reverting it.
func assertHosted(t *testing.T, lang treesitter.Language, probe wrapperProbe, hosted string) {
	t.Helper()
	if hosted == probe.wantHosted {
		return
	}

	var headline, remedy string
	switch {
	case probe.wantHosted == hostedNone:
		headline = "UNRECORDED IMPROVEMENT: a shape no wrapper hosted is now hosted"
		remedy = "flip this probe's wantHosted to the observed set so the fix is recorded in the diff"
	case hosted == hostedNone:
		headline = "REGRESSION: a shape that was hosted is now hosted by no wrapper"
		remedy = "fix the wrappers — do NOT relax wantHosted to record the breakage"
	default:
		headline = "THE HOSTING SET MOVED: the same shape is now hosted by a different set of wrappers"
		remedy = "if the move is intended, flip wantHosted; if not, fix the wrappers"
	}

	t.Errorf("%s.\n"+
		"  lang:     %s\n"+
		"  shape:    %s\n"+
		"  pattern:  %q\n"+
		"  want:     hosted=%s\n"+
		"  observed: hosted=%s\n"+
		"  remedy:   %s",
		headline, lang, probe.shape, probe.pattern, probe.wantHosted, hosted, remedy)
}

// reconcileWrapperRows fails unless the produced rows are exactly what the
// declared probe set calls for. Without it a cell dropped from the table — or
// a subtest that died before appending — would shrink the census silently, and
// every remaining probe would still match its anchor.
func reconcileWrapperRows(t *testing.T, rows []wrapperRow, summaries []wrapperSummary) {
	t.Helper()

	declared := make(map[string]int)
	wantRows := make(map[string]int)
	for _, cell := range wrapperCensusCells {
		cfg, ok := langConfigFor(cell.lang)
		require.Truef(t, ok, "census cell names %s, which has no registered LangConfig", cell.lang)
		for _, probe := range cell.probes {
			key := string(cell.lang) + "\x00" + probe.shape
			declared[key]++
			if probe.unprobeable == "" {
				wantRows[key] = len(cfg.Wrappers)
			}
		}
	}

	gotSummaries := make(map[string]int, len(summaries))
	for _, s := range summaries {
		gotSummaries[s.lang+"\x00"+s.shape]++
	}
	gotRows := make(map[string]int, len(rows))
	for _, r := range rows {
		gotRows[r.lang+"\x00"+r.shape]++
	}

	for key, want := range declared {
		if got := gotSummaries[key]; got != want {
			t.Errorf("declared probe %s produced %d summary rows, want %d", renderProbeKey(key), got, want)
		}
	}
	for key := range gotSummaries {
		if declared[key] == 0 {
			t.Errorf("census summary %s has no declared probe", renderProbeKey(key))
		}
	}
	for key, want := range wantRows {
		if got := gotRows[key]; got != want {
			t.Errorf("declared probe %s produced %d wrapper rows, want one per registered wrapper (%d)",
				renderProbeKey(key), got, want)
		}
	}
	for key := range gotRows {
		if declared[key] == 0 {
			t.Errorf("census rows for %s have no declared probe", renderProbeKey(key))
		}
	}
}

// renderProbeKey makes a probe key readable in a failure message.
func renderProbeKey(key string) string {
	return strings.ReplaceAll(key, "\x00", " | ")
}

// writeWrapperCensus sorts every line and writes the artifact into
// testdata/<name>, where name comes from wrapperCensusEnv. Unset writes
// nothing.
func writeWrapperCensus(t *testing.T, rows []wrapperRow, summaries []wrapperSummary) {
	t.Helper()
	name := os.Getenv(wrapperCensusEnv)
	if name == "" {
		t.Logf("census not written: set %s=<filename> to write the artifact into testdata/", wrapperCensusEnv)
		return
	}
	lines := make([]string, 0, len(rows)+len(summaries))
	for _, r := range rows {
		lines = append(lines, r.line())
	}
	for _, s := range summaries {
		lines = append(lines, s.line())
	}
	sort.Strings(lines)

	require.NoError(t, os.MkdirAll("testdata", 0o750))
	path := filepath.Join("testdata", filepath.Base(name))
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	t.Logf("census written: %s (%d lines)", path, len(lines))
}
