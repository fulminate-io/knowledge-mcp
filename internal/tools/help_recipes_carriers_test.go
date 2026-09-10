// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// help_recipes_carriers_test.go gates the emit identity of every recipe body the
// MODULE SHIPS, across all three carriers rather than the Go help alone.
//
// WHY IT IS MORE THAN ONE. The same body text reaches a user through three
// surfaces: help("recipes"), the /ingest-patterns skill that install-claude-assets
// writes into a consumer's project, and docs/guides/recipes.md. An audit injected
// an identity-less emit into the mirrored skill and the mirrored guide and the
// whole suite stayed green: the skill gate and the guide gate each assert only
// that a body PARSES, and the guide gate read one JSON key so two of the guide's
// three bodies were seen by no gate at all. A defect class closed on one carrier
// and open on the others is closed nowhere.
//
// THE MIRRORS ARE IN-MODULE ON PURPOSE. Every carrier is read through files
// scripts/sync-assets.sh copies inside this module — the embedded assets tree and
// this package's testdata — because the Go test cache tracks in-module inputs
// only. A gate that reached up to .claude/ or docs/guides/ would replay a cached
// pass after its own subject changed. That is not a preference: the guide gate
// used to read the repo root and replayed `ok ... (cached)` three times over a
// corrupted body.
//
// THE SKILL ARM READS THE EMBED MIRROR, WHICH THE HOOK REGENERATES. The
// ingest-patterns skill's bodies reach this table through assets.Files, the tree
// scripts/sync-assets.sh copies out of .claude/skills — and lefthook's
// go-test-staged leg re-runs that script before the tests on any commit staging a
// file under cmd/knowledge. So the bytes this arm censuses are the bytes the
// committing tree ships to a consumer, regenerated at commit time rather than
// trusted from a previous run.

// A recipeCarrier is one shipped surface carrying runnable recipe bodies, with
// the counts its own extractor is expected to return.
//
// THE COUNTS ARE FLOORS THAT ARE RE-DERIVED, never decorations: run the gate and
// read its log line. If one moves for any reason other than adding or removing an
// example, a body left its extractor's reach and THAT is the finding — two bodies
// separated only by a blank line merge into one block that still parses, so the
// count is the only detector.
type recipeCarrier struct {
	name       string
	bodies     []string
	wantBodies int
	wantEmits  int
}

// recipeCarriers assembles the three carriers through the extractors each one
// already had, and fails loudly when a carrier comes back empty or off its count.
func recipeCarriers(t *testing.T) []recipeCarrier {
	t.Helper()

	carriers := []recipeCarrier{
		{
			name:       "the Go help topic",
			bodies:     extractRecipeBlocks(),
			wantBodies: helpRecipesWorkedBodyCount,
			wantEmits:  12,
		},
		{
			name:       "the docs recipes guide",
			bodies:     docsGuideRecipeBodies(),
			wantBodies: 3,
			wantEmits:  3,
		},
		{
			name:       "the ingest-patterns skill",
			bodies:     skillRecipeBodies(t),
			wantBodies: 3,
			wantEmits:  3,
		},
	}
	for _, c := range carriers {
		if len(c.bodies) != c.wantBodies {
			t.Fatalf("%s yielded %d recipe bodies, want %d — the extractor lost a body, or a body was added without updating the count; the census below would be over the wrong population",
				c.name, len(c.bodies), c.wantBodies)
		}
	}
	return carriers
}

// skillRecipeBodies pulls the ingest-patterns skill's recipe bodies out of the
// EMBEDDED assets tree, which is the same read TestSkillRecipeBodies_Parse makes.
//
// IT IS FATAL, NOT SKIPPED, when the embed cannot be read. The parse gate this
// borrows from used to resolve .claude/ at the repo root and t.Skipf when it was
// missing; a carrier that can silently drop out of the table takes its whole
// census arm with it, and the table's counts are the only detector that an arm
// went missing.
func skillRecipeBodies(t *testing.T) []string {
	t.Helper()

	const path = "skills/ingest-patterns/SKILL.md"
	data, err := assets.Files.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s from the embedded assets tree: %v — run scripts/sync-assets.sh", path, err)
	}
	return extractJSONStringValues(string(data), "recipe_body")
}

// TestRecipeCarriers_EveryEmitIdentityIsTheRowsOwnID is the census: every emit
// rule in every body any carrier ships keys its identity on the ROW'S OWN `id`.
//
// IT ASSERTS THE EXPRESSION, NOT THE KEY'S PRESENCE. The predecessor tested
// `_, set := e.Fields["identity"]`, which any non-empty expression satisfies — a
// body written `identity := node.symbol_name` passed it, and passed the fixture
// run too on any fixture whose rows happen to carry distinct symbol names. That
// is the exact defect one step removed: a heading is not unique in the wild. The
// requirement is the row's own id, so that is what this asserts.
//
// THE HEAD IS THE BODY'S OWN because the parser says so: validateBareHeads
// refuses a head that is not in scope for the body at PARSE time, so a body that
// parsed here already has a legal head and this census adds the `.id` tail. That
// is why the parse error below is fatal rather than skipped.
func TestRecipeCarriers_EveryEmitIdentityIsTheRowsOwnID(t *testing.T) {
	censused := 0
	for _, c := range recipeCarriers(t) {
		emits := 0
		for i, b := range c.bodies {
			r, err := recipe.Parse([]byte(b))
			if err != nil {
				t.Errorf("%s body %d does not parse, so its emits cannot be censused: %v\n---\n%s", c.name, i, err, b)
				continue
			}
			for _, rule := range r.Rules {
				e, ok := rule.(recipe.RuleEmit)
				if !ok {
					continue
				}
				emits++
				censused++
				if spelling, keyed := emitIdentityIsRowOwnID(e); !keyed {
					t.Errorf("%s body %d: emit %q at %d:%d keys its identity on %s, want the row's own `<head>.id` — a heading-derived identity is refused on any document that repeats the heading\n---\n%s",
						c.name, i, e.NodeType, e.Pos.Line, e.Pos.Col, spelling, b)
				}
			}
		}
		if emits != c.wantEmits {
			t.Errorf("censused %d emit rules across %s, want %d — an emit was added or removed",
				emits, c.name, c.wantEmits)
		}
		t.Logf("%s: %d bodies, %d emit rules, every identity keyed on the row's own id", c.name, len(c.bodies), emits)
	}
	t.Logf("censused %d emit rules across the shipped carriers in the table", censused)
}

// emitIdentityIsRowOwnID reports whether an emit's `identity` is the two-segment
// `<head>.id` the requirement names, and returns the spelling it found for the
// failure message. An absent field, a literal, a builtin call and a longer path
// (`node.metadata.id`, which reads a metadata key rather than the node's id) all
// come back false.
func emitIdentityIsRowOwnID(e recipe.RuleEmit) (string, bool) {
	expr, set := e.Fields["identity"]
	if !set {
		return "no `identity` field at all, so the stable id is hashed on `name`", false
	}
	f, ok := expr.(recipe.ExprField)
	if !ok || len(f.Path) != 2 || f.Path[1] != "id" {
		return "`" + recipeExprSpelling(expr) + "`", false
	}
	return "`" + strings.Join(f.Path, ".") + "`", true
}

// recipeExprSpelling renders an expression back to something close to its source
// spelling, for a failure message that names what it found rather than a Go type.
func recipeExprSpelling(e recipe.Expr) string {
	switch v := e.(type) {
	case recipe.ExprField:
		return strings.Join(v.Path, ".")
	case recipe.ExprLit:
		return strconv.Quote(v.Value)
	case recipe.ExprVar:
		return v.Name
	case recipe.ExprFunc:
		return v.Name + "(...)"
	case recipe.ExprRegex:
		return recipeExprSpelling(v.LHS) + " ~= /" + v.Pattern + "/"
	}
	return "an expression this test cannot spell"
}

// TestRecipeCarriers_NoBodyIsRefusedForARepeatedIdentity runs every body every
// carrier ships through the REAL collect dispatch, against fixtures that carry
// two sections sharing a heading and two pages sharing a title, and asserts none
// of them is refused for a repeated emit identity.
//
// AND EVERY BODY CARRIES ITS OWN CONTROL, in the same run. A body that matches
// fewer than two rows on these fixtures cannot collide whatever it is keyed on,
// so "not refused" is worth nothing until that body is shown to be REFUSABLE
// through this instrument. The control strips the `identity` field back out of
// each body — the shape it had before the fix — and runs the twin.
//
// EVERY BODY IS ACCOUNTED FOR BY NAME, which is the part an earlier round got
// wrong. The stripper used to remove only a line-initial `identity :=`, so the
// three bodies that write theirs inline after `emit <type> {` were skipped in
// silence and the gate reported "8 of 11" — a number a reader takes for three
// bodies that resisted refusal when three were never offered to the instrument.
// So: a body the census says carries an identity MUST be strippable, and a
// stripped twin the interpreter does not refuse must have matched fewer than two
// rows on every fixture it ran against, which is stated in the log with its row
// count. Neither arm can be satisfied by silence.
func TestRecipeCarriers_NoBodyIsRefusedForARepeatedIdentity(t *testing.T) {
	caller := helpFixtureCaller()
	for _, c := range recipeCarriers(t) {
		refusableTwins := 0
		var unrefusable []string
		for i, b := range c.bodies {
			t.Run(c.name+"/"+firstLine(b), func(t *testing.T) {
				pdfRows, pdfOut := runHelpFixtureExtract(t, caller, "pdf", b)
				webRows, webOut := runHelpFixtureExtract(t, caller, "web", b)
				for _, run := range []struct{ family, out string }{{"pdf", pdfOut}, {"web", webOut}} {
					if strings.Contains(run.out, identityCollisionMarker) {
						t.Errorf("%s body %d is REFUSED on the %s fixture for a repeated emit identity — key the emit on the row's own id\n--- body\n%s\n--- %s output\n%s",
							c.name, i, run.family, b, run.family, run.out)
					}
				}
				t.Logf("%s body %d as shipped: pdf rows=%d web rows=%d", c.name, i, pdfRows, webRows)
			})

			stripped, ok := withoutIdentityField(b)
			if !ok {
				t.Errorf("%s body %d has no `identity` field this control can strip, so it gets no twin run at all — the census in this file asserts every emit carries one, so the stripper is what is wrong here, not the body\n---\n%s",
					c.name, i, b)
				continue
			}
			// THE UNSTRIPPABLE SHAPE IS NAMED BEFORE THE RUN, not diagnosed from
			// its silence afterwards. An emit whose ONLY field is the identity
			// becomes an emit with no fields once the control strips it, which
			// is not a runnable body; the run then produces no extract header
			// and the `!ran` arm below reports the body as covered by NOTHING —
			// true, but it reads as a run that broke for an unknown reason when
			// the cause is a shape this control cannot express a twin for.
			if shapes := identityOnlyEmits(t, b); len(shapes) > 0 {
				t.Errorf("%s body %d carries an UNSTRIPPABLE SHAPE: %s. Removing the identity leaves an emit with no fields at all, so no stripped twin of this body can run and this body is covered by this control by construction rather than by accident. Give the emit a second field, or cover it another way\n---\n%s",
					c.name, i, strings.Join(shapes, "; "), b)
				continue
			}
			refused, maxRows, ran := strippedTwinVerdict(t, caller, stripped)
			switch {
			case refused:
				refusableTwins++
			case !ran:
				t.Errorf("%s body %d: its stripped twin produced no extract header on either fixture, so this body is covered by NOTHING — the run failed for some reason other than the identity collision and the silence above is unearned\n--- stripped twin\n%s",
					c.name, i, stripped)
			case maxRows >= 2:
				t.Errorf("%s body %d: its stripped twin matched %d rows and was NOT refused, so the fixtures no longer carry the duplicate heading and title this whole gate rests on\n--- stripped twin\n%s",
					c.name, i, maxRows, stripped)
			default:
				unrefusable = append(unrefusable,
					fmt.Sprintf("body %d (%s) matched %d rows at most once stripped, too few to collide", i, firstLine(b), maxRows))
			}
		}
		if refusableTwins == 0 {
			t.Errorf("no body in %s is refused by this instrument once its identity is stripped, so the silence above is vacuous — the fixtures no longer carry the duplicate rows, or no body of this carrier reaches two rows on them",
				c.name)
		}
		reason := "every body of this carrier is refusable"
		if len(unrefusable) > 0 {
			reason = "the remainder: " + strings.Join(unrefusable, "; ")
		}
		t.Logf("%s: %d of %d bodies are refused once their identity is stripped — %s", c.name, refusableTwins, len(c.bodies), reason)
	}
}

// identityOnlyEmits names every emit in a body whose ONLY field is the
// identity, in the spelling a reader can find in the body: the emit's node type
// and its source position.
//
// IT PARSES THE BODY RATHER THAN SCANNING ITS TEXT because the shape is about
// how many fields an emit has, and the help writes some emits on one line and
// some across four; a text scan would answer for the layout and not for the
// emit. A body that does not parse is reported as such and yields no shapes,
// which leaves the caller's own parse-failure arms to speak.
func identityOnlyEmits(t *testing.T, body string) []string {
	t.Helper()
	r, err := recipe.Parse([]byte(body))
	if err != nil {
		return nil
	}
	var shapes []string
	for _, rule := range r.Rules {
		e, ok := rule.(recipe.RuleEmit)
		if !ok {
			continue
		}
		if _, keyed := e.Fields["identity"]; keyed && len(e.Fields) == 1 {
			shapes = append(shapes, fmt.Sprintf("emit %q at %d:%d has `identity` as its only field",
				e.NodeType, e.Pos.Line, e.Pos.Col))
		}
	}
	return shapes
}

// strippedTwinVerdict runs one stripped twin against both fixture families and
// reports what the instrument saw: whether the interpreter refused it for a
// repeated identity, the largest row count any family returned, and whether ANY
// family produced an extract header at all.
//
// THE THIRD RETURN IS WHAT KEEPS THE SECOND HONEST. runHelpFixtureExtract
// returns -1 both for a refusal and for a run that produced no header, so a body
// whose twin failed for an unrelated reason would otherwise read as "matched too
// few rows to collide" — a body covered by nothing, reported as a body that
// cannot collide.
func strippedTwinVerdict(t *testing.T, caller *recipeRoutingCaller, stripped string) (refused bool, maxRows int, ran bool) {
	t.Helper()
	maxRows = -1
	for _, family := range []string{"pdf", "web"} {
		rows, out := runHelpFixtureExtract(t, caller, family, stripped)
		if strings.Contains(out, identityCollisionMarker) {
			refused = true
		}
		if rows >= 0 {
			ran = true
			if rows > maxRows {
				maxRows = rows
			}
		}
	}
	return refused, maxRows, ran
}

// identityFieldPattern matches an `identity := <head>.<field>` emit field
// WHEREVER IT SITS, line-initial or inline after `emit <type> {`, with or
// without the trailing comma that separates it from the next field.
//
// IT IS A REGEXP RATHER THAN A LINE PREFIX because a line prefix was wrong. The
// help writes three of its emit blocks on one line — `emit pattern { type :=
// "pattern", identity := page.id,` — so a stripper anchored at the start of the
// line found nothing to remove in them and their twins never ran.
//
// THE EXPRESSION SHAPE IS THE ONE THE CENSUS ADMITS: the two-segment
// `<head>.id` that TestRecipeCarriers_EveryEmitIdentityIsTheRowsOwnID requires.
// A body keying its identity on a literal or a call fails that census first, and
// here it yields no match — reported as false, which the caller treats as a
// failure rather than as a body to skip.
var identityFieldPattern = regexp.MustCompile(`[ \t]*identity[ \t]*:=[ \t]*[A-Za-z0-9_.]+[ \t]*,?[ \t]*\n?`)

// withoutIdentityField deletes every `identity := ...` emit field from a body,
// which is what the body looked like before the fix. It reports false when there
// was nothing to delete, so a caller can tell "removed" from "nothing to remove"
// — and every caller here treats "nothing to remove" as a defect in this
// stripper, because the census asserts every shipped emit carries one.
func withoutIdentityField(body string) (string, bool) {
	stripped := identityFieldPattern.ReplaceAllString(body, "")
	return stripped, stripped != body
}

// walkIllustrationRowCount is the number of rows the walk illustration returns
// over the pdf fixture: the fixture's TWO section nodes, s1 and s2, both
// admitted by the illustration's kind filter. The leaves beneath s1 (p1, cb1,
// tb1) are walked and then dropped by that filter.
//
// IT IS A LITERAL READ OFF THE FIXTURE, not a number this package computes. The
// count is what makes the filter observable: without it the same body returns 5
// rows, three of them nameless, which is not what the illustration claims to
// show a reader.
//
// IT PINS BOTH CARRIERS THAT SHIP THE BODY. The Go help's copy is pinned by the
// gate below and the docs guide's by TestDocsGuide_WalkExampleEmitsOnlyItsNamedRows,
// each running its own carrier's bytes; the round that added the filter to one
// copy and not the other is why the guide has its own run rather than inheriting
// this number from the help's.
const walkIllustrationRowCount = 2

// TestHelpRecipes_WalkIllustrationEmitsOnlyItsNamedRows pins that row count.
//
// WHY THE ILLUSTRATION NEEDS A FILTER AT ALL. Setting an emit identity defeats
// the empty-name skip — evalEmit drops a row only when name AND identity are both
// empty — so the identity this change added turned the illustration's three
// nameless leaves from skipped rows into emitted ones. The illustration is an
// outline; an outline of a document is its headings. The kind filter is the same
// one every reading-loop example carries.
func TestHelpRecipes_WalkIllustrationEmitsOnlyItsNamedRows(t *testing.T) {
	// SELECTED BY ITS EMIT'S OWN FIELD LIST, which is unique to it and unchanged
	// by this test's subject: the reading-loop examples that also walk CONTAINS
	// carry a `pos := walk.position` between these two lines. Selecting on the
	// filter itself would make the locator part of what is under test.
	body := workedBodyContaining(t, "    level := walk.depth\n    page := node.page_first\n}")
	rows, out := runHelpFixtureExtract(t, helpFixtureCaller(), "pdf", body)
	if rows != walkIllustrationRowCount {
		t.Errorf("the grammar's walk illustration returned %d rows over the pdf fixture, want %d — the fixture's two section nodes and nothing else\n--- body\n%s\n--- out\n%s",
			rows, walkIllustrationRowCount, body, out)
	}
	if skipped := helpFixtureExtractStat(out, "skipped"); skipped > 0 {
		t.Errorf("the walk illustration skipped %d rows; a body carrying an identity skips none, so this is a different body than the one under test\n%s", skipped, out)
	}
	t.Logf("walk illustration: rows=%d over the pdf fixture", rows)
}

// TestRecipeCarriers_NoEmitReadsAPageBody is the census form of the defect the
// docs guide's first example carried: an emit field reading a page's body.
//
// A PAGE NODE HAS NO BODY IN EITHER TEXT FIELD. emitPageNode
// (collector/web/emit_nodes.go) writes Id, Type, SymbolName, Source and Metadata
// and nothing else, because each paragraph, code block, list and table is its own
// node holding its own text; the web package's TestEmit_PageIsItsChunksNotItsBody
// asserts it over a real crawl. So `body := page.description` renders an empty
// column on every real graph — the guide shipped exactly that, and the fixture
// hid it by carrying a Description no collector writes.
//
// IT READS EMIT FIELDS AND SAYS SO. A where-tree naming the same field is a
// filter that matches nothing rather than a column of blanks, which is a
// different (and self-announcing) mistake; this gate is about what a reader
// copies expecting to see text.
func TestRecipeCarriers_NoEmitReadsAPageBody(t *testing.T) {
	censused := 0
	for _, c := range recipeCarriers(t) {
		for i, b := range c.bodies {
			r, err := recipe.Parse([]byte(b))
			if err != nil {
				t.Errorf("%s body %d does not parse: %v", c.name, i, err)
				continue
			}
			for _, rule := range r.Rules {
				e, ok := rule.(recipe.RuleEmit)
				if !ok {
					continue
				}
				for field, expr := range e.Fields {
					f, isField := expr.(recipe.ExprField)
					if !isField || len(f.Path) != 2 || f.Path[0] != "page" {
						continue
					}
					censused++
					if f.Path[1] == "description" || f.Path[1] == "body" {
						t.Errorf("%s body %d: emit %q reads `%s` into %q — a page node carries no body in Content or Description, so this column is empty on every real crawl\n---\n%s",
							c.name, i, e.NodeType, strings.Join(f.Path, "."), field, b)
					}
				}
			}
		}
	}
	t.Logf("censused %d emit fields reading a bare `page.<field>` across the shipped carriers", censused)
}
