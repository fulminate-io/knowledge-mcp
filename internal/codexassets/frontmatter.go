// SPDX-License-Identifier: Apache-2.0

// frontmatter.go — YAML-frontmatter parsing for the codex-asset
// translation library. Mirrors parseInstructionFrontmatter
// (cmd/knowledge/internal/bootstrap/instruction_bootstrap.go:197)
// byte-for-byte on the marker-split algorithm, but the translator needs
// a WIDER frontmatter struct than the bootstrap helper's unexported
// Name+Description-only type: agent .md→.toml conversion reads the
// extra Claude-only fields (model/tools/skills/argument-hint) so the
// emitter can decide what to OMIT from the codex TOML.
//
// Why a copy of the 22-line split logic instead of calling the
// bootstrap helper: that helper lives in package bootstrap with an
// unexported narrow struct. Copying the split loop is cheaper than
// exporting + widening the bootstrap type, which would broaden the
// bootstrap API surface for this translation library.

package codexassets

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatter is the YAML frontmatter block at the top of
// .claude/agents/*.md and .claude/skills/*/SKILL.md files. It is wider
// than bootstrap.instructionFrontmatter (Name+Description only) because
// the codex agent emitter reads the Claude-only fields to decide what
// to drop from the generated TOML (model/sandbox/mcp_servers inherit
// the parent codex session, so they are omitted).
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
// Markers are `---` on their own line. Mirrors
// parseInstructionFrontmatter: when there is no leading `---` line, or
// no closing `---`, or the YAML block fails to unmarshal, it returns
// (zero, full-content, false). On success it returns the parsed
// frontmatter, the body after the closing marker, and true.
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
