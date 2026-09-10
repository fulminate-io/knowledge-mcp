// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// help_recipes_identity_test.go pins the EMIT IDENTITY of every recipe body the
// help text ships.
//
// THE DEFECT CLASS. emitIdentity (cmd/knowledge/internal/recipe/interpret_emit.go)
// takes the emit block's `identity` field, then its `name`, then the row's own
// NodeID; evalEmit refuses the WHOLE RUN at the first repeated identity. Every
// shipped body used to key on a heading-derived name, so each was refused on any
// document carrying that heading twice — a title page and a copyright page
// repeating a book's title is enough. A live run reproduced it on a real book.
//
// The fix is one line per body, `identity := <row>.id`. These gates are what
// stop it coming back: the three-carrier census and interpret gates in
// help_recipes_carriers_test.go, the interpreted runs in the sibling fixture
// gate, the two prose illustrations that no block extractor can see, and the
// sentences in the rendered help that tell a reader why.

// identityCollisionMarker is the distinctive fragment of the interpreter's OWN
// duplicate-identity refusal.
//
// IT IS RETYPED, AND THAT IS WHY THE CONTROL BELOW EXISTS. The format string is
// an unexported literal in another package, so no import can reach it; a marker
// that silently stopped matching would turn every gate that reads it into a
// vacuous pass. TestHelpRecipes_IdentityCollisionMarkerDiscriminates therefore
// fires the marker through the production dispatch with a body engineered to
// collide, and shows it silent on the same body keyed on a unique field, in the
// same run.
const identityCollisionMarker = "produced identity"

// THE CENSUS LIVES IN help_recipes_carriers_test.go NOW, and it grew two ways
// getting there: it runs over all THREE carriers the module ships rather than
// the Go help alone, and it asserts the identity EXPRESSION is the row's own
// `<head>.id` rather than that an `identity` key is set. The help-only,
// presence-only predecessor was a strict subset of both and is gone rather than
// kept as a second thing to keep in step; the help carrier's expected emit count
// moved onto that gate's carrier table.

// TestHelpRecipes_IdentityCollisionMarkerDiscriminates is the same-run control
// for identityCollisionMarker: it fires on a refusal and is silent on a clean
// run, through the production collect dispatch and against the same fixtures the
// other gates use.
//
// WITHOUT IT the marker is an unverified string, and every "no collision"
// assertion in this package would pass on a marker that no longer matches
// anything. The colliding body names EVERY ROW THE SAME THING, which is the
// defect class in its purest form; the clean body differs from it by the one
// line this change adds to the help.
func TestHelpRecipes_IdentityCollisionMarkerDiscriminates(t *testing.T) {
	caller := helpFixtureCaller()
	const colliding = "select section\n" +
		"emit probe { name := \"one name for every row\" }\n"
	const unique = "select section\n" +
		"emit probe { identity := section.id, name := \"one name for every row\" }\n"

	rows, out := runHelpFixtureExtract(t, caller, "pdf", colliding)
	if rows != -1 || !strings.Contains(out, identityCollisionMarker) {
		t.Fatalf("a body naming every row the same was NOT refused with %q (rows=%d) — the fixture lost its duplicate rows, or the refusal moved\n%s",
			identityCollisionMarker, rows, out)
	}
	uniqueRows, uniqueOut := runHelpFixtureExtract(t, caller, "pdf", unique)
	if uniqueRows < 2 {
		t.Fatalf("the same body keyed on section.id returned %d rows, want at least 2 — the control proves nothing if it did not run\n%s", uniqueRows, uniqueOut)
	}
	if strings.Contains(uniqueOut, identityCollisionMarker) {
		t.Fatalf("the marker fired on a clean run, so it does not discriminate\n%s", uniqueOut)
	}
	t.Logf("marker %q: refusal yes / clean run no, over %d clean rows", identityCollisionMarker, uniqueRows)
}

// TestHelpRecipes_ClauseIllustrationsResolveAsAPair runs the emit and lookup
// CLAUSE ILLUSTRATIONS out of the grammar section against a fixture.
//
// THEY ARE PROSE, NOT BLOCKS, so no other gate in this package can see them:
// extractRecipeBlocks wants a four-space-indented run beginning with `select`,
// and these are one-line fragments inside backticks. A reader copies them all
// the same.
//
// THE PAIR IS THE SUBJECT. lookup recomputes the StableID the emit produced, so
// its identity expression has to be the emit's. If one of the two moves without
// the other, every lookup misses — silently, since a miss only skips the
// downstream link. This gate reads lookups_resolved off the run's own header
// for exactly that reason, and would not notice the breakage from a row count.
func TestHelpRecipes_ClauseIllustrationsResolveAsAPair(t *testing.T) {
	emitClause := backtickSpanStartingWith(t, helpRecipesGrammar, "emit pattern {")
	lookupClause := backtickSpanStartingWith(t, helpRecipesGrammar, "lookup pattern by ")
	body := "select page\n" + emitClause + "\n" + lookupClause + "\n"

	caller := helpFixtureCaller()
	rows, out := runHelpFixtureExtract(t, caller, "web", body)
	if rows < 2 {
		t.Fatalf("the grammar's emit + lookup illustrations returned %d rows over the web fixture's two pages\n--- body\n%s\n--- out\n%s", rows, body, out)
	}
	resolved := helpFixtureExtractStat(out, "lookups_resolved")
	misses := helpFixtureExtractStat(out, "lookup_misses")
	if resolved < rows || misses != 0 {
		t.Errorf("the lookup illustration does not resolve what the emit illustration wrote: lookups_resolved=%d lookup_misses=%d over %d rows — the two clauses must key on the same identity expression\n--- body\n%s\n--- out\n%s",
			resolved, misses, rows, body, out)
	}
	t.Logf("clause pair: rows=%d lookups_resolved=%d lookup_misses=%d", rows, resolved, misses)
}

// TestHelpRecipes_CanonicalPipelineLookupResolves is the same coupling on the
// help's canonical cross-ref pipeline, which is a real block and does run
// through the sibling fixture gate — but that gate reads rows only, and a
// pipeline whose lookup and link never fire still emits its patterns and still
// reports rows. The lookup counter is the only observable that separates them.
func TestHelpRecipes_CanonicalPipelineLookupResolves(t *testing.T) {
	body := workedBodyContaining(t, "lookup pattern by ")
	caller := helpFixtureCaller()
	rows, out := runHelpFixtureExtract(t, caller, "web", body)
	if rows < 1 {
		t.Fatalf("the canonical cross-ref pipeline returned %d rows\n--- body\n%s\n--- out\n%s", rows, body, out)
	}
	if resolved := helpFixtureExtractStat(out, "lookups_resolved"); resolved < 1 {
		t.Errorf("the canonical pipeline's lookup resolved %d of the patterns its own emit wrote — the lookup's identity expression has to be the emit's\n--- body\n%s\n--- out\n%s",
			resolved, body, out)
	}
	t.Logf("canonical pipeline: rows=%d lookups_resolved=%d", rows, helpFixtureExtractStat(out, "lookups_resolved"))
}

// identityUniquenessSentence is the rule the help owes a reader who is writing
// their own body: what identity is, that it must be unique per row, and what to
// key it on. Held here whitespace-folded, because the help wraps its prose and a
// reader reads the rendering rather than the wrapping.
const identityUniquenessSentence = "`identity` is what the emitted node's stable id is hashed on, " +
	"and it must be UNIQUE PER ROW: two rows resolving to one identity refuse " +
	"the whole run at the first collision. It defaults to `name`, which is a " +
	"heading on most documents and is therefore not unique — key it on the " +
	"row's own `id` unless you have a better unique field."

// TestHelp_RecipesTopicStatesTheIdentityUniquenessRule asserts the sentence is
// in the RENDERED topic, once.
//
// ON THE RENDERING, NOT THE CONSTANT. handleHelpClient is what a help() call
// reaches, and the topic is composed from four constants — a sentence added to
// a constant that the composition dropped would satisfy a source-string
// assertion and reach no reader. ONCE, because the same sentence in two places
// is two things to keep in step.
func TestHelp_RecipesTopicStatesTheIdentityUniquenessRule(t *testing.T) {
	folded := foldWhitespace(renderedRecipesTopic(t))
	if n := strings.Count(folded, identityUniquenessSentence); n != 1 {
		t.Errorf("help(\"recipes\") states the identity-uniqueness rule %d times, want exactly 1:\n%s", n, identityUniquenessSentence)
	}
}

// renderedRecipesTopic returns what a help("recipes") CALL renders, which is
// what a reader sees. The topic is composed from four constants, so a sentence
// added to a constant the composition dropped would satisfy a source-string
// assertion and reach nobody.
func renderedRecipesTopic(t *testing.T) string {
	t.Helper()
	args, err := json.Marshal(map[string]string{"topic": "recipes"})
	if err != nil {
		t.Fatal(err)
	}
	res := handleHelpClient(args)
	if res.IsError {
		t.Fatalf("help(recipes) returned an error: %q", resultText(res))
	}
	return resultText(res)
}

// backtickSpanStartingWith returns the text of the first backtick-delimited span
// in s that begins with prefix, whitespace-folded so the help's own line
// wrapping does not reach the recipe body a reader would paste.
func backtickSpanStartingWith(t *testing.T, s, prefix string) string {
	t.Helper()
	idx := strings.Index(s, "`"+prefix)
	if idx < 0 {
		t.Fatalf("no backticked span beginning %q in the help text — the illustration was reworded or removed", prefix)
	}
	rest := s[idx+1:]
	end := strings.Index(rest, "`")
	if end < 0 {
		t.Fatalf("the backticked span beginning %q is not closed", prefix)
	}
	return foldWhitespace(rest[:end])
}

// workedBodyContaining returns the one worked body carrying the given fragment,
// failing when zero or more than one does — an ambiguous match would test a
// different body than the caller named.
func workedBodyContaining(t *testing.T, fragment string) string {
	t.Helper()
	var found []string
	for _, b := range extractRecipeBlocks() {
		if strings.Contains(b, fragment) {
			found = append(found, b)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d worked bodies contain %q, want exactly 1", len(found), fragment)
	}
	return found[0]
}

// helpFixtureExtractStat reads one `key=<int>` counter off an extract header —
// the run's own disclosure, in the same shape helpFixtureExtractRows reads
// rows=. Returns -1 when there is no header or no such counter, which keeps a
// refusal distinguishable from a counter that really is zero.
func helpFixtureExtractStat(out, key string) int {
	for line := range strings.SplitSeq(out, "\n") {
		if !strings.HasPrefix(line, "extract:") {
			continue
		}
		for f := range strings.FieldsSeq(line) {
			rest, ok := strings.CutPrefix(f, key+"=")
			if !ok {
				continue
			}
			n, err := strconv.Atoi(rest)
			if err != nil {
				return -1
			}
			return n
		}
	}
	return -1
}

// foldWhitespace collapses every run of whitespace to one space, which is how a
// wrapped help paragraph and a one-line rule fragment are compared without
// pinning the wrapping.
func foldWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// postTraverseHeadSentence is the help's claim about which bare heads are legal
// after a traverse with no alias, held whitespace-folded because the help wraps.
//
// ITS PREDECESSOR WAS FALSE: it said `node` was the ONLY legal head there, while
// the parser's own refusal message enumerates four and `page.id` runs and
// produces the same identities. A reader who tried the spelling the help called
// illegal got a working run.
const postTraverseHeadSentence = "After a `traverse` with no `as` alias the legal heads are " +
	"`edge`, `group`, `node` and `page` — `section` is not one of them — and " +
	"because a bare head names the current row, `node.id` and `page.id` " +
	"produce the SAME per-row identities here."

// TestHelpRecipes_PostTraverseHeadClaimIsTrue checks that sentence AGAINST THE
// PARSER AND THE INTERPRETER, not against another sentence.
//
// Three assertions, each on the production path: every head the sentence
// enumerates parses in the illustration's own emit; the head it says is not one
// of them is a PARSE ERROR; and the two heads it calls interchangeable produce
// byte-identical extract output, identities included, over the web fixture.
func TestHelpRecipes_PostTraverseHeadClaimIsTrue(t *testing.T) {
	body := workedBodyContaining(t, "link $doc --[contains]--> $sec")
	const shipped = "identity := node.id"
	if strings.Count(body, shipped) != 1 {
		t.Fatalf("the hierarchical emit body carries %d occurrences of %q, want exactly 1 — the substitutions below would hit the wrong emit\n%s",
			strings.Count(body, shipped), shipped, body)
	}

	for _, head := range []string{"edge", "group", "node", "page"} {
		variant := strings.Replace(body, shipped, "identity := "+head+".id", 1)
		if _, err := recipe.Parse([]byte(variant)); err != nil {
			t.Errorf("the help says `%s` is a legal head after a traverse with no alias, but the parser refuses it: %v", head, err)
		}
	}
	illegal := strings.Replace(body, shipped, "identity := section.id", 1)
	if _, err := recipe.Parse([]byte(illegal)); err == nil {
		t.Errorf("the help says `section` is NOT a legal head after a traverse with no alias, but `identity := section.id` parses")
	}

	caller := helpFixtureCaller()
	_, nodeOut := runHelpFixtureExtract(t, caller, "web", body)
	_, pageOut := runHelpFixtureExtract(t, caller, "web",
		strings.Replace(body, shipped, "identity := page.id", 1))
	// The header carries the run's own wall-clock elapsed_ms, which two runs of
	// the same fixture legitimately differ in (measured 2 vs 0 on the OSS mirror
	// job); the claim here is about the identities, so the comparison masks that
	// one field with the same control the replay test uses.
	if maskElapsed(t, nodeOut) != maskElapsed(t, pageOut) {
		t.Errorf("the help says `node.id` and `page.id` produce the same per-row identities here, but the two runs differ\n--- node.id\n%s\n--- page.id\n%s", nodeOut, pageOut)
	}

	folded := foldWhitespace(renderedRecipesTopic(t))
	if n := strings.Count(folded, postTraverseHeadSentence); n != 1 {
		t.Errorf("help(\"recipes\") states the post-traverse head rule %d times, want exactly 1:\n%s", n, postTraverseHeadSentence)
	}
}
