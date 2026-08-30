// SPDX-License-Identifier: Apache-2.0

package codexassets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestTranslateAgent_AllClaudeAgents walks every .claude/agents/*.md and
// runs TranslateAgent on each, asserting the output is valid codex TOML:
// it unmarshals, developer_instructions is emitted as a multiline basic
// string ("""), name + description are non-empty, and no `model` key
// survives (codex inherits the parent session's model). This replaces
// the all-agents coverage the deleted build-time generator used to
// prove — install-codex-assets now translates these same files at
// install time, so every one MUST translate cleanly.
func TestTranslateAgent_AllClaudeAgents(t *testing.T) {
	root := repoRoot(t)
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
			out, err := TranslateAgent(md)
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
			if di, _ := m["developer_instructions"].(string); di == "" {
				t.Errorf("%s: developer_instructions empty", name)
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
	_, err := TranslateAgent([]byte("# Just a heading\n\nNo frontmatter here.\n"))
	if err == nil {
		t.Fatal("TranslateAgent returned nil error on frontmatter-less input, want error")
	}
	if !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("error %q does not mention frontmatter", err)
	}
}

// TestParseFrontmatter_AllClaudeSkills walks every .claude/skills/<name>/SKILL.md
// and asserts its YAML frontmatter parses with the keys the seeding path reads.
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
			// The seeding path names a skill node after its DIRECTORY, so a
			// frontmatter name that disagrees with the directory would seed a
			// node nothing can find by the name users type.
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
