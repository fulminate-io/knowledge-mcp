// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

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
func TestSkillRecipeBodies_Parse(t *testing.T) {
	root := recipesRepoRoot(t)
	path := filepath.Join(root, ".claude", "skills", "ingest-patterns", "SKILL.md")
	data, err := os.ReadFile(path) //nolint:gosec // trusted in-repo skill path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	bodies := extractRecipeBodyPayloads(string(data))
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

// extractRecipeBodyPayloads pulls the JSON string value of every
// "recipe_body" key out of the skill markdown and unescapes it, so the
// bytes tested are the bytes a user would paste.
func extractRecipeBodyPayloads(s string) []string {
	var out []string
	const key = `"recipe_body":`
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

func recipesRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, ".claude", "skills")); err != nil {
		t.Skipf("canonical .claude/skills not found at %s: %v", root, err)
	}
	return root
}
