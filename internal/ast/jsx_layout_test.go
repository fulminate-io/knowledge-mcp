// SPDX-License-Identifier: Apache-2.0

// jsx_layout_test.go — the JSX layout-whitespace reproduction.
//
// THE DEFECT THESE REPRODUCE. A JSX element that is a CHILD of another JSX
// element and is preceded by whitespace CONTAINING A NEWLINE did not match —
// which is essentially all real-world formatted JSX. The same element preceded
// by spaces only, by nothing at all, or sitting outside a JSX parent matched
// fine.
//
// THE MECHANISM, measured rather than assumed. The tsx and javascript grammars
// absorb newline-bearing inter-child whitespace into the LEADING ANONYMOUS
// TOKEN of the following node: the `<` of a jsx_self_closing_element spans
// "\n<" and the `</` of a jsx_closing_element spans "\n</". CHILD COUNTS STAY
// EQUAL, so child alignment succeeded and the rejection happened one level down,
// at the childless-token text comparison, which compared "<" against "\n<".
// Space-only whitespace behaves differently again — it becomes a separate named
// jsx_text sibling — because JSX renders a single space between children and
// discards newline-bearing whitespace. testdata/jsx_layout_mechanism.txt is the
// measurement; TestJSXLayoutMechanism below keeps it honest.
//
// WHY THE PER-GRAMMAR LAYOUT-TOKEN SKIP WAS NOT THE SEAM. That skip is a
// CHILD-LIST filter: it drops whole children from both lists. There was no child
// to drop here, so no generalization of it reaches this defect. The fix is the
// whitespace-trimmed anonymous-token comparison in layout_jsx.go, gated by
// LangConfig.TrimsAnonTokenWhitespace, whose own catchers live in
// layout_jsx_test.go.
//
// WHY IT WAS NEVER CAUGHT. Every other JSX fixture in this package is
// single-line, and the layout census's tsx probe is a plain TypeScript function
// body containing no JSX at all.
//
// THE RED-FIRST IDIOM, mirroring honesty_repro_test.go. Each reproduction was
// authored in its CORRECT-BEHAVIOR form, run against the unfixed tree, and the
// raw failing run committed at testdata/jsx_layout_red.txt; only then was the
// assertion INVERTED to state the observed brokenness. Each was inverted back to
// assert correct behavior by the phase that fixed the defect. The committed test
// is GREEN in every one of those states and the suite is never left red, while
// the assertion stays anchored to a measured outcome — it fails on a NEW defect
// and equally on an UNRECORDED change of behavior, so no partial or accidental
// fix can land silently. Read each test's own anchor marker for where it stands.
//
// THE ANCHOR MARKER. Each reproduction carries exactly one marker line
// immediately above its func:
//
//	// JSX-LAYOUT-ANCHOR <TestName>: broken    <- as first written, asserting the defect
//	// JSX-LAYOUT-ANCHOR <TestName>: correct   <- after the owning phase inverts it
//
// Inverting a reproduction means flipping BOTH the assertion and its marker, in
// the commit of the phase that owns the fix. Real markers end at the verdict
// word; the two illustrative lines above deliberately do not, because the
// close-out greps count marker lines unanchored so that a RENAMED marker still
// moves the count.

package ast

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// jsxChildPattern is the reported shape: a self-closing element with one
// captured attribute value.
const jsxChildPattern = "<CodeBlock code={$C} />"

// jsxParentPattern is the enclosing-element shape, whose closing tag carries
// the same absorption on its own leading token.
const jsxParentPattern = "<div>$$$K</div>"

// jsxNewlineChild is the defect fixture: the child is preceded by a newline.
const jsxNewlineChild = "const m = <div>\n<CodeBlock code={a} />\n</div>;\n"

// jsxAdjacentChild is the sharpest control — the same construct differing only
// in the whitespace before the child, which does match.
const jsxAdjacentChild = "const n = <div><CodeBlock code={a} />\n</div>;\n"

// jsxNoWhitespaceAtAll carries no inter-child whitespace on either side, so it
// is the known positive for the closing-tag reproduction.
const jsxNoWhitespaceAtAll = "const q = <div><CodeBlock code={a} /></div>;\n"

// jsxLayoutGrammars is every JSX-bearing grammar these reproductions run under.
// javascript is here because the root cause is shared: .jsx files resolve to the
// javascript grammar and reproduce identically, so a tsx-only fix would leave
// half the reported surface broken.
var jsxLayoutGrammars = []struct {
	label string
	cfg   LangConfig
}{
	{label: "tsx", cfg: tsxLangConfig},
	{label: "javascript", cfg: jsLangConfig},
}

// JSX-LAYOUT-ANCHOR TestJSXLayoutNewlineChild: correct
func TestJSXLayoutNewlineChild(t *testing.T) {
	for _, g := range jsxLayoutGrammars {
		t.Run(g.label+"_newline_child", func(t *testing.T) {
			control := runLongTailWalker(t, g.cfg, jsxChildPattern, jsxAdjacentChild)
			require.Len(t, control, 1,
				"known positive: the same child with no preceding newline matches, so a zero below would be a refusal and not a dead probe")

			matches := runLongTailWalker(t, g.cfg, jsxChildPattern, jsxNewlineChild)
			assert.Len(t, matches, 1,
				"a newline before a JSX child is layout, not a constraint — the pattern must reach it")
		})
	}
}

// JSX-LAYOUT-ANCHOR TestJSXLayoutNewlineBeforeClosingTag: correct
func TestJSXLayoutNewlineBeforeClosingTag(t *testing.T) {
	for _, g := range jsxLayoutGrammars {
		t.Run(g.label+"_newline_before_closing_tag", func(t *testing.T) {
			control := runLongTailWalker(t, g.cfg, jsxParentPattern, jsxNoWhitespaceAtAll)
			require.Len(t, control, 1,
				"known positive: the same element with no whitespace before its closing tag matches")

			matches := runLongTailWalker(t, g.cfg, jsxParentPattern, jsxAdjacentChild)
			assert.Len(t, matches, 1,
				"the newline before a closing tag is absorbed into the `</` token and must not constrain the match — the enclosing-element shape is a distinct authoring case and does not ride along on the child one")
		})
	}
}

// TestJSXLayoutKnownPositives is NOT anchored: every row is green before and
// after the fix. These are characterization guards — they pin the boundary of
// the defect, so a fix that over-widens (matching where the grammar draws a
// real distinction) fails here rather than passing quietly.
func TestJSXLayoutKnownPositives(t *testing.T) {
	rows := []struct {
		name   string
		source string
		want   int
	}{
		// Space-only whitespace becomes a separate named jsx_text sibling
		// rather than being absorbed into the following token, so this matches
		// today. It proves whitespace per se is not the trigger.
		{name: "space_separated_child", source: "const s = <div> <CodeBlock code={a} /> </div>;\n", want: 1},
		{name: "adjacent_child", source: jsxNoWhitespaceAtAll, want: 1},
		// A newline outside any JSX parent has no preceding sibling whose
		// leading token could absorb it, which is what makes the defect
		// child-position-specific.
		{name: "newline_outside_jsx_parent", source: "const o =\n  <CodeBlock code={a} />;\n", want: 1},
		{name: "jsx_text_bearing_parent", source: "const p = <div>x\n  <CodeBlock code={a} />\n</div>;\n", want: 1},
	}
	for _, g := range jsxLayoutGrammars {
		for _, row := range rows {
			t.Run(g.label+"_"+row.name, func(t *testing.T) {
				matches := runLongTailWalker(t, g.cfg, jsxChildPattern, row.source)
				assert.Len(t, matches, row.want, "source: %q", row.source)
			})
		}
	}

	// The attribute-discrimination row, mirroring anon_token_test.go's
	// tsx_bare_div_still_matches. It fails on CHILD COUNT — the attributed
	// element carries an extra jsx_attribute child — so it cannot fire on a
	// whitespace-comparison mistake and is not this defect's catcher. It is here
	// as the must-not-regress control: a bare element must keep matching.
	t.Run("attribute_discrimination_survives", func(t *testing.T) {
		source := "function App() {\n  return <div className=\"x\">{attributed}</div>;\n}\n" +
			"function Bare() {\n  return <div>{plain}</div>;\n}\n"
		matches := runLongTailWalker(t, tsxLangConfig, "<div>{$C}</div>", source)
		assert.Equal(t, []string{"plain"}, capturedTexts(matches, "C"),
			"an attributed element must stay excluded while the bare one still matches")
	})
}

// ---------------------------------------------------------------------------
// THE MECHANISM MEASUREMENT.
//
// The reproductions above pin WHAT is broken. This measurement pins WHY, as a
// durable artifact rather than as prose, because the seam the fix belongs at
// follows from the mechanism and from nothing else. Three mechanisms could
// produce the same black-box symptom, and they have three different seams:
//
//   - anonymous_child       — the multi-line spelling carries an EXTRA anonymous
//     child. Seam: the child-list filter, which is what closed the equivalent
//     Go defect.
//   - node_padding          — the whitespace sits inside the node's byte range
//     but outside every child. Seam: span normalization.
//   - token_span_absorption — child lists are equal and the whitespace lives
//     INSIDE the leading anonymous token's own byte range. Seam: the
//     childless-token text comparison, and nothing upstream of it.
//
// The classifier below decides between them from the measurement. It is
// deliberately not a constant: an artifact asserting a verdict nobody measured
// is the hand list this package refuses everywhere. A run that emits anything
// other than token_span_absorption is a REPORTABLE surprise — do not edit the
// artifact to agree with it.

// jsxMechanismEnv names the environment variable that enables the artifact
// write. Unset means "measure and compare, write nothing".
const jsxMechanismEnv = "AST_JSX_MECHANISM_WRITE"

// jsxMechanismName is the committed artifact under testdata/.
const jsxMechanismName = "jsx_layout_mechanism.txt"

// The three mechanisms the measurement can distinguish, plus the two outcomes
// that mean the measurement did not land on any of them.
const (
	jsxMechAbsorption   = "token_span_absorption"
	jsxMechAnonChild    = "anonymous_child"
	jsxMechNodePadding  = "node_padding"
	jsxMechUnclassified = "unclassified"
	jsxMechDisagreement = "node_kinds_disagree"
)

// jsxChildSpan is one child of a measured node, dumped in full: a mechanism
// argued from summary counts alone is not auditable later.
type jsxChildSpan struct {
	index int
	kind  string
	named bool
	extra bool
	start uint32
	end   uint32
	text  string
}

// jsxNodeSpan is one measured node in one spelling.
type jsxNodeSpan struct {
	kind            string
	start           uint32
	end             uint32
	childCount      int
	firstChildStart uint32
	firstChildText  string
	children        []jsxChildSpan
}

// measureJSXNode parses src under lang and dumps the FIRST node of the given
// kind. Both spellings hold exactly one of each measured kind, so "first" is
// unambiguous.
func measureJSXNode(t *testing.T, lang treesitter.Language, src, kind string) jsxNodeSpan {
	t.Helper()
	tree, buf, ok := parseClean(t, lang, src)
	require.True(t, ok, "%s: fixture must parse cleanly, or the dump measures a recovery tree", lang)
	defer tree.Close()

	var found *sitter.Node
	walkAll(tree.RootNode(), func(n *sitter.Node) {
		if found == nil && n != nil && n.Type() == kind {
			found = n
		}
	})
	require.NotNil(t, found, "%s: no %s in %q", lang, kind, src)

	out := jsxNodeSpan{
		kind:       kind,
		start:      found.StartByte(),
		end:        found.EndByte(),
		childCount: int(found.ChildCount()),
	}
	for i := range out.childCount {
		c := found.Child(i)
		if c == nil {
			continue
		}
		out.children = append(out.children, jsxChildSpan{
			index: i,
			kind:  c.Type(),
			named: c.IsNamed(),
			extra: c.IsExtra(),
			start: c.StartByte(),
			end:   c.EndByte(),
			text:  c.Content(buf),
		})
	}
	if len(out.children) > 0 {
		out.firstChildStart = out.children[0].start
		out.firstChildText = out.children[0].text
	}
	return out
}

// classifyJSXMechanism decides which mechanism the two spellings exhibit, and
// returns the reason alongside it. The order of the tests is the order in which
// the mechanisms are DISTINGUISHABLE: an extra child is visible in the counts, a
// padded span is visible in the node-versus-first-child offsets, and absorption
// is what remains when neither is true and the leading token differs only by
// surrounding whitespace.
func classifyJSXMechanism(multi, single jsxNodeSpan) (string, string) {
	switch {
	case multi.childCount > single.childCount:
		return jsxMechAnonChild, fmt.Sprintf(
			"the multi-line spelling carries %d children against the one-line spelling's %d, so the whitespace surfaces as its own child",
			multi.childCount, single.childCount)
	case multi.start < multi.firstChildStart:
		return jsxMechNodePadding, fmt.Sprintf(
			"the node starts at %d but its first child starts at %d, so the whitespace lies inside the node and outside every child",
			multi.start, multi.firstChildStart)
	case multi.firstChildText != single.firstChildText &&
		strings.TrimSpace(multi.firstChildText) == strings.TrimSpace(single.firstChildText):
		return jsxMechAbsorption, fmt.Sprintf(
			"child counts are equal at %d and the node starts exactly at its first child, whose text differs from the one-line spelling's only by surrounding whitespace (%q against %q)",
			multi.childCount, multi.firstChildText, single.firstChildText)
	default:
		return jsxMechUnclassified, fmt.Sprintf(
			"neither an extra child (%d against %d), nor padding (node %d, first child %d), nor a whitespace-only token difference (%q against %q)",
			multi.childCount, single.childCount, multi.start, multi.firstChildStart,
			multi.firstChildText, single.firstChildText)
	}
}

// jsxMechanismKinds are the two node kinds that carry the defect: the child
// element's own opening token and the enclosing element's closing tag. Both are
// measured because the closing-tag half is a distinct authoring shape and must
// not be assumed to ride along on the child half.
var jsxMechanismKinds = []string{"jsx_self_closing_element", "jsx_closing_element"}

// jsxMechanismHeader prefixes the artifact. It states what the file is for, so a
// later reader is not left inferring the measurement's purpose from its rows.
const jsxMechanismHeader = `# JSX layout-whitespace mechanism census.
#
# Measured, never asserted. For each JSX-bearing grammar this records the same
# two node kinds parsed from a newline-bearing spelling and from a spelling with
# no inter-child whitespace, dumps every child of each with its byte span and
# exact source text, and classifies the difference into one of three mechanisms:
# anonymous_child, node_padding or token_span_absorption. The matcher seam that
# can fix the defect follows from which one holds.
#
# Regenerate with:
#   AST_JSX_MECHANISM_WRITE=1 go test ./cmd/knowledge/internal/ast/ -run TestJSXLayoutMechanism
#
# A run that classifies anything other than token_span_absorption is a finding to
# report, not an artifact to overwrite.

`

// TestJSXLayoutMechanism measures the mechanism for every JSX grammar and
// compares the committed artifact against the fresh measurement.
func TestJSXLayoutMechanism(t *testing.T) {
	var lines []string
	for _, g := range jsxLayoutGrammars {
		lang := g.cfg.Lang
		perKind := map[string]string{}
		var detail []string

		for _, kind := range jsxMechanismKinds {
			multi := measureJSXNode(t, lang, jsxNewlineChild, kind)
			single := measureJSXNode(t, lang, jsxNoWhitespaceAtAll, kind)
			mech, why := classifyJSXMechanism(multi, single)
			perKind[kind] = mech
			detail = append(detail, fmt.Sprintf("lang=%s node=%s mechanism=%s why=%s", g.label, kind, mech, why))
			detail = append(detail, jsxSpanLines(g.label, "multi", multi)...)
			detail = append(detail, jsxSpanLines(g.label, "single", single)...)
		}

		verdict := perKind[jsxMechanismKinds[0]]
		for _, kind := range jsxMechanismKinds[1:] {
			if perKind[kind] != verdict {
				verdict = jsxMechDisagreement
			}
		}
		lines = append(lines,
			fmt.Sprintf("lang=%s mechanism=%s self_closing=%s closing=%s",
				g.label, verdict, perKind["jsx_self_closing_element"], perKind["jsx_closing_element"]))
		lines = append(lines, detail...)
		lines = append(lines, "")

		assert.Equal(t, jsxMechAbsorption, verdict,
			"%s: the seam this defect's fix belongs at follows from the mechanism. "+
				"Do NOT edit the artifact to agree with a different verdict — stop and report it.", g.label)
	}

	compareJSXMechanismArtifact(t, lines)
}

// jsxSpanLines renders one measured node as its summary row plus one row per
// child.
func jsxSpanLines(label, spelling string, n jsxNodeSpan) []string {
	out := []string{fmt.Sprintf("lang=%s node=%s spelling=%s span=[%d,%d) childCount=%d firstChildStart=%d",
		label, n.kind, spelling, n.start, n.end, n.childCount, n.firstChildStart)}
	for _, c := range n.children {
		out = append(out, fmt.Sprintf(
			"lang=%s node=%s spelling=%s child=%d kind=%q named=%t extra=%t span=[%d,%d) text=%q",
			label, n.kind, spelling, c.index, c.kind, c.named, c.extra, c.start, c.end, c.text))
	}
	return out
}

// compareJSXMechanismArtifact fails unless the committed artifact matches the
// fresh measurement, and writes it when jsxMechanismEnv is set. Mirrors
// compareCensusArtifact: this measurement is hermetic — every fixture is an
// inline snippet — so it can compare rather than only record.
func compareJSXMechanismArtifact(t *testing.T, lines []string) {
	t.Helper()
	want := jsxMechanismHeader + strings.Join(lines, "\n")

	path := filepath.Join("testdata", jsxMechanismName)
	if os.Getenv(jsxMechanismEnv) != "" {
		require.NoError(t, os.MkdirAll("testdata", 0o750))
		require.NoError(t, os.WriteFile(path, []byte(want), 0o600))
		t.Logf("mechanism census written: %s (%d rows)", path, len(lines))
		return
	}

	got, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
	require.NoError(t, err, "mechanism artifact missing — regenerate with %s=1", jsxMechanismEnv)
	require.Equal(t, want, string(got),
		"mechanism artifact is stale — regenerate with %s=1 go test ./cmd/knowledge/internal/ast/ -run TestJSXLayoutMechanism", jsxMechanismEnv)
}
