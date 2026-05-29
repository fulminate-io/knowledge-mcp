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
