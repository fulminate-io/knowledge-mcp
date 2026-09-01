// SPDX-License-Identifier: Apache-2.0

package codexassets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestTranslateAgent_ResolvedPathsPreamble pins the install-time preamble:
// it is rendered from the shared const with the EXACT root the caller
// passed, the body follows verbatim after one blank line, and an empty
// root is an error rather than a preamble naming nothing.
//
// Every expected string is built as fmt.Sprintf(resolvedPathsPreamble, …)
// — referencing the const BY NAME. That is what enforces the single
// authoritative declaration: a const that is absent, renamed, or replaced
// by an inline literal in the emitter fails to compile here or fails
// outright. A re-typed literal in this file would silently restore the
// two-copies defect.
func TestTranslateAgent_ResolvedPathsPreamble(t *testing.T) {
	const root = "/probe/codex/skills"
	fixture := "---\nname: probe\ndescription: A probe agent.\n---\n# Heading\n\nBody line.\n"

	// The parsed body is the comparison subject, not the raw markdown
	// suffix: parseFrontmatter consumes the newline terminating the closing
	// `---`, so the parsed body is one leading newline shorter.
	_, parsedBody, ok := parseFrontmatter(fixture)
	if !ok {
		t.Fatal("fixture frontmatter does not parse")
	}

	t.Run("renders_the_exact_root", func(t *testing.T) {
		out, err := TranslateAgent([]byte(fixture), root)
		if err != nil {
			t.Fatalf("TranslateAgent: %v", err)
		}
		var m map[string]any
		if err := toml.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		di, _ := m["developer_instructions"].(string)
		want := fmt.Sprintf(resolvedPathsPreamble, root)
		if !strings.HasPrefix(di, want) {
			t.Errorf("developer_instructions does not begin with the preamble rendered for %q:\n want prefix %q\n got        %q", root, want, di)
		}
	})

	t.Run("body_follows_verbatim", func(t *testing.T) {
		out, err := TranslateAgent([]byte(fixture), root)
		if err != nil {
			t.Fatalf("TranslateAgent: %v", err)
		}
		var m map[string]any
		if err := toml.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		di, _ := m["developer_instructions"].(string)
		want := fmt.Sprintf(resolvedPathsPreamble, root) + "\n\n" + parsedBody
		if di != want {
			t.Errorf("developer_instructions is not preamble + blank line + body verbatim:\n want %q\n got  %q", want, di)
		}
	})

	t.Run("empty_root_errors", func(t *testing.T) {
		if _, err := TranslateAgent([]byte(fixture), ""); err == nil {
			t.Fatal("TranslateAgent returned nil error on an empty skills root, want error")
		} else if !strings.Contains(err.Error(), "skills root") {
			t.Errorf("error %q does not mention the skills root", err)
		}
	})
}

// TestTranslateAgent_AllClaudeAgents walks every .claude/agents/*.md and
// runs TranslateAgent on each, asserting the output is valid codex TOML:
// it unmarshals, developer_instructions is emitted as a multiline basic
// string ("""), name + description are non-empty, no `model` key
// survives (codex inherits the parent session's model), and each carries
// the install-time preamble rendered for the root it was translated
// against. This replaces the all-agents coverage the deleted build-time
// generator used to prove — install-codex-assets now translates these
// same files at install time, so every one MUST translate cleanly.
func TestTranslateAgent_AllClaudeAgents(t *testing.T) {
	root := repoRoot(t)
	const skillsRoot = "/probe/all-agents/skills"
	agentsDir := filepath.Join(root, ".claude", "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		t.Fatalf("read agents dir: %v", err)
	}

	count := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			md, err := os.ReadFile(filepath.Join(agentsDir, name)) //nolint:gosec // trusted .claude/agents fixture path
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			out, err := TranslateAgent(md, skillsRoot)
			if err != nil {
				t.Fatalf("TranslateAgent(%s): %v", name, err)
			}
			if !strings.Contains(string(out), `developer_instructions = """`) {
				t.Errorf("%s: developer_instructions not emitted as a multiline basic string:\n%s", name, out)
			}
			var m map[string]any
			if err := toml.Unmarshal(out, &m); err != nil {
				t.Fatalf("%s: emitted TOML does not unmarshal: %v\n---\n%s", name, err, out)
			}
			if s, _ := m["name"].(string); s == "" {
				t.Errorf("%s: name empty, want non-empty", name)
			}
			if s, _ := m["description"].(string); s == "" {
				t.Errorf("%s: description empty, want non-empty", name)
			}
			di, _ := m["developer_instructions"].(string)
			if di == "" {
				t.Errorf("%s: developer_instructions empty", name)
			}
			if want := fmt.Sprintf(resolvedPathsPreamble, skillsRoot); !strings.HasPrefix(di, want) {
				t.Errorf("%s: developer_instructions does not begin with the install-time preamble:\n want prefix %q", name, want)
			}
			if _, present := m["model"]; present {
				t.Errorf("%s: model key present, want omitted (codex inherits parent default)", name)
			}
			count++
		})
	}
	if count == 0 {
		t.Fatal("no .claude/agents/*.md found to translate")
	}
}

// TestTranslateAgent_NoFrontmatter returns a clear error rather than
// emitting a degenerate TOML when the input has no parseable frontmatter.
func TestTranslateAgent_NoFrontmatter(t *testing.T) {
	_, err := TranslateAgent([]byte("# Just a heading\n\nNo frontmatter here.\n"), "/probe/skills")
	if err == nil {
		t.Fatal("TranslateAgent returned nil error on frontmatter-less input, want error")
	}
	if !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("error %q does not mention frontmatter", err)
	}
}

// TestParseFrontmatter_AllClaudeSkills walks every .claude/skills/<name>/SKILL.md
// and asserts its YAML frontmatter parses with the keys this package reads.
//
// WHY THIS EXISTS AS A SEPARATE WALK. The sibling agent walk covers
// .claude/agents/*.md only, and codexContent deliberately passes skill
// files through VERBATIM — a skill is not a codex subagent, so nothing
// translates it. The consequence was that no test in the repo parsed a
// SKILL.md at all: a skill could ship with frontmatter that does not
// parse and every gate would stay green. That happened — a description
// scalar picked up a colon-space, which is illegal in a YAML plain
// scalar, and the whole header stopped parsing while twelve
// content-level greps over the same file continued to pass. Greps over a
// structured file verify a LINE; only a parse verifies the DOCUMENT.
//
// The NESTED layout is the one the repo ships and the one this asserts:
// .claude/skills/<name>/SKILL.md, not a flat .claude/skills/*.md.
func TestParseFrontmatter_AllClaudeSkills(t *testing.T) {
	root := repoRoot(t)
	skillsDir := filepath.Join(root, ".claude", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatalf("read skills dir: %v", err)
	}

	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		if _, statErr := os.Stat(path); statErr != nil {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			md, readErr := os.ReadFile(path) //nolint:gosec // trusted .claude/skills fixture path
			if readErr != nil {
				t.Fatalf("read %s: %v", path, readErr)
			}
			fm, body, ok := parseFrontmatter(string(md))
			if !ok {
				t.Fatalf("%s: frontmatter does not parse", e.Name())
			}
			if fm.Name == "" {
				t.Errorf("%s: frontmatter name empty, want non-empty", e.Name())
			}
			if fm.Description == "" {
				t.Errorf("%s: frontmatter description empty, want non-empty", e.Name())
			}
			// A skill is addressed by its DIRECTORY name — that is what both
			// installers write and what a user types — so a frontmatter name
			// disagreeing with the directory names the skill two ways at once.
			if fm.Name != e.Name() {
				t.Errorf("%s: frontmatter name %q != directory name %q", e.Name(), fm.Name, e.Name())
			}
			if strings.TrimSpace(body) == "" {
				t.Errorf("%s: body empty after frontmatter", e.Name())
			}
			count++
		})
	}
	if count == 0 {
		t.Fatal("no .claude/skills/*/SKILL.md found to parse")
	}
}
