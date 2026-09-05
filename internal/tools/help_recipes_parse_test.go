// SPDX-License-Identifier: Apache-2.0

package tools

import (
	_ "embed"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// docsRecipesGuide is the MIRRORED copy of docs/guides/recipes.md that
// scripts/sync-assets.sh writes into this package's testdata, gitignored like
// the other mirrors.
//
// THE EMBED IS THE WHOLE POINT, not a convenience. The gate below used to
// resolve the repo root at run time and os.ReadFile the guide, which sits ABOVE
// this module — and the Go test cache tracks IN-MODULE inputs only. Measured on
// this tree: corrupting the guide's recipe body and re-running the same -run key
// replayed `ok ... (cached)` three times, while a cache-miss run of the very
// same tree failed. An embed is a build input, so a change to the mirror
// rebuilds the package and this gate re-reads the bytes it exists to check.
//
//go:embed testdata/recipes-guide.md
var docsRecipesGuide []byte

// TestHelpRecipes_WorkedExamplesParse runs recipe.Parse over every complete
// recipe body embedded in the help text users read.
//
// WHY THIS EXISTS. These bodies ship to users as things to copy, and until
// now nothing parsed them — the grammar could tighten underneath them and
// no gate would notice. That is not hypothetical: validateBareHeads is a
// parse-time rejection added after most of this text was written, so every
// example became subject to a validator that had never been applied to it.
// A documented example that no longer parses is a live defect whose only
// detector is a user hitting it.
//
// It parses only COMPLETE bodies — a contiguous indented run beginning with
// `select`. The one-line rule illustrations elsewhere in the help are
// fragments, and wrapping a fragment in invented context would test the
// wrapper rather than the documentation; see the sibling test's comment.
func TestHelpRecipes_WorkedExamplesParse(t *testing.T) {
	blocks := extractRecipeBlocks(helpRecipes)
	if len(blocks) == 0 {
		t.Fatal("no worked recipe bodies found in helpRecipes — extractor is broken or the help lost its examples")
	}
	for i, b := range blocks {
		t.Run(firstLine(b), func(t *testing.T) {
			if _, err := recipe.Parse([]byte(b)); err != nil {
				t.Errorf("help worked example %d does not parse: %v\n---\n%s", i, err, b)
			}
		})
	}
	t.Logf("parsed %d worked recipe bodies from helpRecipes", len(blocks))
}

// TestSkillRecipeBodies_Parse runs recipe.Parse over every inline
// `recipe_body` payload in the /ingest-patterns skill. Those are literal
// strings a user is instructed to paste, so one that does not parse is a
// broken instruction shipped to every consumer.
//
// IT READS THE EMBEDDED ASSETS TREE, not the repo-root source, and that is
// strictly more correct as well as cache-correct: assets.Files is the tree
// install-claude-assets writes verbatim, so this gate now covers the bytes that
// actually reach a consumer. The former repo-root resolver also carried a
// t.Skipf on a missing .claude/skills — a silent-skip path this gate no longer
// has.
func TestSkillRecipeBodies_Parse(t *testing.T) {
	const path = "skills/ingest-patterns/SKILL.md"
	data, err := assets.Files.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s from the embedded assets tree: %v — run scripts/sync-assets.sh", path, err)
	}
	bodies := extractJSONStringValues(string(data), "recipe_body")
	if len(bodies) == 0 {
		t.Fatal("no recipe_body payloads found in SKILL.md — extractor is broken or the skill lost its examples")
	}
	for i, b := range bodies {
		t.Run(firstLine(b), func(t *testing.T) {
			if _, err := recipe.Parse([]byte(b)); err != nil {
				t.Errorf("skill recipe_body %d does not parse: %v\n---\n%s", i, err, b)
			}
		})
	}
	t.Logf("parsed %d recipe_body payloads from SKILL.md", len(bodies))
}

// extractRecipeBlocks pulls contiguous indented runs that begin with
// `select` out of help prose. Indentation is the block marker the help
// text uses for code.
//
// IT SKIPS EBNF. The help also carries a grammar whose production for the
// select rule reads `select = "select" IDENT [ "where" expr ] .` — indented,
// and starting with the word select, so a naive extractor picks it up and
// reports the GRAMMAR as a body that does not parse. That is a false
// positive in the extractor, not a defect in the docs, and it fired on the
// first run here. A production line is recognized by its ` = ` separator,
// which no recipe rule uses.
func extractRecipeBlocks(s string) []string {
	var out []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n")+"\n")
			cur = nil
		}
	}
	for line := range strings.SplitSeq(s, "\n") {
		indented := strings.HasPrefix(line, "    ")
		trimmed := strings.TrimSpace(line)
		switch {
		case len(cur) == 0 && indented && strings.HasPrefix(trimmed, "select ") &&
			!strings.Contains(trimmed, " = "):
			cur = append(cur, strings.TrimPrefix(line, "    "))
		case len(cur) > 0 && (indented || trimmed == ""):
			cur = append(cur, strings.TrimPrefix(line, "    "))
		case len(cur) > 0:
			flush()
		}
	}
	flush()
	return out
}

// TestDocsRecipeExamples_Parse runs recipe.Parse over the recipe body shipped
// in the docs guide.
//
// NO GATE HAS EVER PARSED IT, which is precisely how the legacy example there
// drifted: the guide's body sits as the JSON string value of a "content" key
// inside a two-space-indented block, so extractRecipeBlocks (which wants a
// four-space indent and a leading `select `) and the skill extractor (which
// keyed on the literal "recipe_body") both returned ZERO over it — measured,
// against a same-run control of four bodies from helpRecipes.
func TestDocsRecipeExamples_Parse(t *testing.T) {
	bodies := extractJSONStringValues(string(docsRecipesGuide), "content")
	// THE FATAL-ON-EMPTY GUARD ITS TWO SIBLINGS ALREADY CARRY. Without it a run
	// that extracted nothing prints a PASS line, and the gate is satisfied by a
	// test that parsed no bytes at all.
	if len(bodies) == 0 {
		t.Fatal("no recipe bodies found in docs/guides/recipes.md — extractor is broken or the guide lost its example")
	}
	for i, b := range bodies {
		t.Run(firstLine(b), func(t *testing.T) {
			if _, err := recipe.Parse([]byte(b)); err != nil {
				t.Errorf("docs guide recipe %d does not parse: %v\n---\n%s", i, err, b)
			}
		})
	}
	t.Logf("parsed %d recipe bodies from docs/guides/recipes.md", len(bodies))
}

// extractJSONStringValues pulls the JSON string value of every occurrence of
// the named key out of markdown and unescapes it, so the bytes tested are the
// bytes a user would paste.
//
// IT IS PARAMETERIZED ON THE KEY rather than duplicated per carrier: the skill
// keys on "recipe_body" and the docs guide on "content", and two near-copies of
// this scanner would drift the moment one of them was fixed.
func extractJSONStringValues(s, key string) []string {
	var out []string
	key = `"` + key + `":`
	for idx := strings.Index(s, key); idx >= 0; idx = strings.Index(s, key) {
		s = s[idx+len(key):]
		q := strings.Index(s, `"`)
		if q < 0 {
			return out
		}
		rest := s[q:]
		var decoded string
		// Grow the candidate span until it unmarshals as a JSON string.
		for end := 1; end < len(rest); end++ {
			if rest[end] != '"' || rest[end-1] == '\\' {
				continue
			}
			if json.Unmarshal([]byte(rest[:end+1]), &decoded) == nil {
				out = append(out, decoded)
				s = rest[end+1:]
				break
			}
		}
	}
	return out
}

func firstLine(s string) string {
	before, _, found := strings.Cut(s, "\n")
	if found {
		return before
	}
	return s
}
