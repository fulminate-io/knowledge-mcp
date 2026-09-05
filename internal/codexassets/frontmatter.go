// SPDX-License-Identifier: Apache-2.0

// frontmatter.go — YAML-frontmatter parsing for the codex-asset
// translation library, and the client's ONLY frontmatter parser.
//
// The parse is deliberately narrow in what it promises and wide in what
// it reads: markers are `---` on their own line, and the struct carries
// every field the agent .md→.toml conversion needs — including the
// Claude-only ones (model/tools/skills/argument-hint) — because the
// emitter decides what to OMIT from the codex TOML by reading them.
//
// The parser is TOLERANT BY DESIGN rather than strict: a file with no
// leading `---`, no closing `---`, or an unparseable YAML block comes
// back (zero, full-content, false) rather than as an error. Callers
// decide what a false means for them — TranslateAgent treats it as a
// failed install, while a caller that only wants a body can take the
// content unchanged.

package codexassets

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatter is the YAML frontmatter block at the top of
// .claude/agents/*.md and .claude/skills/*/SKILL.md files. It carries
// more than name+description because the codex agent emitter reads the
// Claude-only fields to decide what to drop from the generated TOML
// (model/sandbox/mcp_servers inherit the parent codex session, so they
// are omitted).
type frontmatter struct {
	Name         string      `yaml:"name"`
	Description  string      `yaml:"description"`
	Model        string      `yaml:"model"`
	Tools        string      `yaml:"tools"`
	Skills       stringSlice `yaml:"skills"`
	ArgumentHint string      `yaml:"argument-hint"`
}

// stringSlice tolerates a YAML field that is either a scalar string or
// a list of strings. .claude agents declare `skills:` as a YAML list;
// future authors might use a single scalar. Either decodes cleanly.
type stringSlice []string

// UnmarshalYAML accepts both a scalar and a sequence node.
func (s *stringSlice) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var single string
		if err := value.Decode(&single); err != nil {
			return err
		}
		if single != "" {
			*s = []string{single}
		}
		return nil
	}
	var many []string
	if err := value.Decode(&many); err != nil {
		return err
	}
	*s = many
	return nil
}

// parseFrontmatter splits a markdown file into (frontmatter, body, ok).
// Markers are `---` on their own line. When there is no leading `---`
// line, or no closing `---`, or the YAML block fails to unmarshal, it
// returns (zero, full-content, false) — the body is handed back whole so
// a caller that only wants text loses nothing. On success it returns the
// parsed frontmatter, the body after the closing marker, and true.
func parseFrontmatter(content string) (frontmatter, string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return frontmatter{}, content, false
	}
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return frontmatter{}, content, false
	}
	yamlBlock := strings.Join(lines[1:closeIdx], "\n")
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return frontmatter{}, content, false
	}
	body := strings.Join(lines[closeIdx+1:], "\n")
	return fm, body, true
}
