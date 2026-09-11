// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"
)

// help_recipes_twins_test.go gates the TWIN RELATION between recipe bodies that
// more than one carrier ships.
//
// WHY IT EXISTS, from the defect it was written after. The same recipe body
// reaches a reader through the Go help topic, through docs/guides/recipes.md and
// through the /ingest-patterns skill. A change applied to one copy and not to the
// others is invisible to every gate in this package: the census reads each body's
// emit fields, and the interpreted runs read each body's rows, so two copies that
// disagree both pass on their own terms. That is exactly what happened — the walk
// illustration gained a kind filter in the Go help while its byte-identical copy
// in the guide kept returning three nameless leaves under prose calling it an
// outline.
//
// SO THE PAIRS ARE NAMED HERE, and each one is asserted equal modulo whitespace.
// A treatment applied to one member and not the other fails this gate naming both
// carriers, which is the shape a one-carrier fix takes when it is caught rather
// than shipped. Deliberately diverging a pair is legitimate and its repair is to
// retire the pair from this table with the reason, never to leave the table
// disagreeing with the tree.

// walkIllustrationFragment locates the walk illustration in the Go help, and is
// the same locator its row-count gate uses: the emit's own two closing field
// lines, which no other worked body carries (the reading-loop examples that also
// walk CONTAINS carry a `pos := walk.position` between them).
const walkIllustrationFragment = "    level := walk.depth\n    page := node.page_first\n}"

// recipeBodyTwin is one body more than one shipped carrier carries, named by
// the side that locates each copy. The `why` line is what a reader of a failure
// needs: what the pair is for, so a diverging change is judged rather than
// merely reverted.
type recipeBodyTwin struct {
	name  string
	left  recipeBodySide
	right recipeBodySide
	why   string
}

// recipeBodySide locates one copy: the carrier's own extractor plus the
// fragment that picks the body out of it.
//
// IT CARRIES TWO NAMES FOR ONE CARRIER, and the second is not redundant.
// `carrier` is what a failure message says, and says the most useful thing it
// can — the guide is named by its path because that is where a reader goes.
// `corpusName` is the name recipeCarriers knows the same surface by, which is
// what the derived-pairing gate joins on; a side whose two names name different
// surfaces resolves to no body there and is fatal rather than silent.
type recipeBodySide struct {
	carrier    string
	corpusName string
	bodies     func(t *testing.T) []string
	fragment   string
}

func goHelpSide(fragment string) recipeBodySide {
	return recipeBodySide{
		carrier:    "the Go help topic",
		corpusName: "the Go help topic",
		bodies:     func(t *testing.T) []string { t.Helper(); return extractRecipeBlocks() },
		fragment:   fragment,
	}
}

func guideSide(fragment string) recipeBodySide {
	return recipeBodySide{
		carrier:    "docs/guides/recipes.md",
		corpusName: "the docs recipes guide",
		bodies:     func(t *testing.T) []string { t.Helper(); return docsGuideRecipeBodies() },
		fragment:   fragment,
	}
}

func skillSide(fragment string) recipeBodySide {
	return recipeBodySide{
		carrier:    "the ingest-patterns skill",
		corpusName: "the ingest-patterns skill",
		bodies:     skillRecipeBodies,
		fragment:   fragment,
	}
}

// recipeBodyTwins is the census, taken over every body of all three carriers:
// seventeen bodies, of which these two are carried twice. Every other body is
// carried once — the Go help's ten remaining worked bodies, the guide's page
// example, and the skill's two Idempotent-filtered ingest bodies, whose emit
// types and filters differ from every other body in the tree.
var recipeBodyTwins = []recipeBodyTwin{
	{
		name:  "the walk illustration",
		left:  goHelpSide(walkIllustrationFragment),
		right: guideSide("level := walk.depth"),
		why: "both teach one rule for a whole nested outline over a pdf, so the kind filter that makes " +
			"the rowset an outline, and the row count pinned on it, belong to both copies",
	},
	{
		name:  "the section-outline body",
		left:  guideSide("path := heading_path"),
		right: skillSide("emit reference {"),
		why: "the guide's scratch-file example and the skill's first ingest body are the same body; the " +
			"Go help ships no copy of it, so the pair is the guide against the skill",
	},
}

// TestRecipeCarriers_TwinBodiesAreTheSameBody asserts each censused pair is one
// body written twice rather than two bodies that used to agree.
func TestRecipeCarriers_TwinBodiesAreTheSameBody(t *testing.T) {
	agreed := 0
	for _, tw := range recipeBodyTwins {
		left := foldBodyLines(oneBodyContaining(t, tw.left.carrier, tw.left.bodies(t), tw.left.fragment))
		right := foldBodyLines(oneBodyContaining(t, tw.right.carrier, tw.right.bodies(t), tw.right.fragment))
		if left != right {
			t.Errorf("%s differs between %s and %s — a treatment applied to one copy and not the other is invisible to every other gate here, because each copy passes the census and the interpreted run on its own terms.\nWHY THE PAIR EXISTS: %s\n--- %s\n%s\n--- %s\n%s",
				tw.name, tw.left.carrier, tw.right.carrier, tw.why,
				tw.left.carrier, left, tw.right.carrier, right)
			continue
		}
		agreed++
	}
	// THE SUMMARY LINE STATES THE FAILURE OR IT IS NOT EMITTED. An unconditional
	// "N pairs agree" printed beneath N-1 agreeing pairs and one red is a line
	// that contradicts the failure directly above it, and a reader skimming a
	// long log takes the summary for the verdict.
	if agreed < len(recipeBodyTwins) {
		t.Logf("%d of %d twin pairs agree line for line; the %d named above DO NOT", agreed, len(recipeBodyTwins), len(recipeBodyTwins)-agreed)
		return
	}
	t.Logf("%d twin pairs across the three carriers agree line for line", agreed)
}

// TestDocsGuide_WalkExampleEmitsOnlyItsNamedRows pins the guide's copy of the
// walk illustration to the SAME literal row count its Go-help twin is pinned to.
//
// WHY THE GUIDE NEEDS ITS OWN RUN. The twin gate above proves the two bodies
// agree; it cannot prove either of them returns what its prose claims. This runs
// the guide's own bytes through the real collect dispatch, so the number in the
// gate is read off the guide's copy rather than inherited from the help's.
func TestDocsGuide_WalkExampleEmitsOnlyItsNamedRows(t *testing.T) {
	body := guideBodyContaining(t, "level := walk.depth")
	rows, out := runHelpFixtureExtract(t, helpFixtureCaller(), "pdf", body)
	if rows != walkIllustrationRowCount {
		t.Errorf("the guide's walk example returned %d rows over the pdf fixture, want %d — the fixture's two section nodes and nothing else; without the kind filter the walk also emits the three leaves beneath the first section, and an identity keeps them rather than skipping them\n--- body\n%s\n--- out\n%s",
			rows, walkIllustrationRowCount, body, out)
	}
	if skipped := helpFixtureExtractStat(out, "skipped"); skipped > 0 {
		t.Errorf("the guide's walk example skipped %d rows; a body carrying an identity skips none, so this is a different body than the one under test\n%s", skipped, out)
	}
	t.Logf("the guide's walk example: rows=%d over the pdf fixture", rows)
}

// guideBodyContaining returns the one docs-guide body carrying the fragment,
// failing when zero or more than one does — the guide's sibling of
// workedBodyContaining, and fatal for the same reason: an ambiguous match tests
// a different body than the caller named.
func guideBodyContaining(t *testing.T, fragment string) string {
	t.Helper()
	return oneBodyContaining(t, "docs/guides/recipes.md", docsGuideRecipeBodies(), fragment)
}

// oneBodyContaining is the shared selector both locators above call.
func oneBodyContaining(t *testing.T, carrier string, bodies []string, fragment string) string {
	t.Helper()
	var found []string
	for _, b := range bodies {
		if strings.Contains(b, fragment) {
			found = append(found, b)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d bodies of %s contain %q, want exactly 1", len(found), carrier, fragment)
	}
	return found[0]
}

// foldBodyLines trims trailing whitespace off every line and drops the blank
// lines at either end, which is the only difference the carriers' own extractors
// impose: the help's block extractor keeps the blank lines that separated the
// block from the prose, and a JSON string value has none.
func foldBodyLines(body string) string {
	var kept []string
	for line := range strings.SplitSeq(body, "\n") {
		kept = append(kept, strings.TrimRight(line, " \t"))
	}
	for len(kept) > 0 && kept[0] == "" {
		kept = kept[1:]
	}
	for len(kept) > 0 && kept[len(kept)-1] == "" {
		kept = kept[:len(kept)-1]
	}
	return strings.Join(kept, "\n")
}
