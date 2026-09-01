// SPDX-License-Identifier: Apache-2.0

// agent_toml.go — the ONE codex-agent-format-specific unit. emitAgentTOML
// converts a parsed Claude agent (.claude/agents/<name>.md frontmatter +
// markdown body) into a codex subagent TOML
// (~/.codex/agents/<name>.toml). All format risk is concentrated here:
// if the codex agent convention changes, only this function moves.
//
// Conversion contract (LOCKED, verified against codex-cli 0.134.0 +
// https://developers.openai.com/codex/subagents):
//   - REQUIRED keys: name, description, developer_instructions.
//   - developer_instructions = an install-time resolved-paths preamble,
//     a blank line, then the markdown body VERBATIM. Codex interprets
//     Claude constructs ($ARGUMENTS, Agent(...), mcp__knowledge__*)
//     fine — no body rewriting.
//   - OMIT model / model_reasoning_effort / sandbox_mode / mcp_servers:
//     codex inherits them from the parent session. .claude declares
//     `model: opus`, a Claude model meaningless to codex — do NOT map
//     opus→a gpt model.
//
// WHY THE PREAMBLE, and why it is emitted rather than embedded: agent
// definitions mandate reads of files under ~/.claude/skills/, but a codex
// install puts the skills tree under its OWN root (dest.skills, default
// ~/.agents/skills). Codex parses a `skills:` frontmatter key only to
// discard it, so there is no other route by which a mandated read
// resolves. The root is known only at install time, which is why the
// preamble is rendered here from the caller's skillsRoot instead of being
// baked into any checked-in text.
//
// go-toml/v2 (already a dep) does the marshaling. developer_instructions
// carries the ,multiline tag so the body is emitted as a TOML multiline
// basic string (""" ... """) — readable real line breaks, matching the
// codex subagents doc's own example shape — rather than one giant
// escaped single-line string. Output is deterministic across runs and
// round-trips back to the original body via toml.Unmarshal.

package codexassets

import (
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

// resolvedPathsPreamble is the SINGLE authoritative declaration of the
// install-time resolved-paths preamble. It is kept unbroken on one line,
// and the emitter and its tests both reference it BY NAME rather than
// re-typing the text — that is what keeps one copy authoritative, since a
// second literal would drift the moment either side is edited.
const resolvedPathsPreamble = "Resolved install paths: the skills root on this machine is %s. Instructions that name paths under ~/.claude/skills/ resolve under this root instead."

// TranslateAgent converts a raw .claude agent markdown document (YAML
// frontmatter + body) into a codex subagent TOML document. It is the
// install-time entry point: install-codex-assets reads each
// agents/<name>.md out of the shared assets.Files embed and runs it
// through TranslateAgent to produce the on-disk <name>.toml.
//
// skillsRoot is the resolved on-disk skills root of the install in
// progress; it is rendered into the preamble that leads
// developer_instructions. An empty skillsRoot is an ERROR (raised by
// emitAgentTOML), never a preamble that names nothing.
//
// Returns a clear error when the frontmatter is missing or unparseable
// (parseFrontmatter ok=false) so a malformed agent file fails the
// install loudly rather than emitting a TOML with empty name/description.
func TranslateAgent(md []byte, skillsRoot string) ([]byte, error) {
	fm, body, ok := parseFrontmatter(string(md))
	if !ok {
		return nil, fmt.Errorf("agent markdown has no parseable YAML frontmatter")
	}
	return emitAgentTOML(fm, body, skillsRoot)
}

// codexAgent is the codex subagent TOML shape. Field order in the
// struct fixes the key order in marshaled output (go-toml/v2 emits in
// struct-declaration order), which keeps emitAgentTOML deterministic so
// sync regeneration is byte-stable.
type codexAgent struct {
	Name                  string `toml:"name"`
	Description           string `toml:"description"`
	DeveloperInstructions string `toml:"developer_instructions,multiline"`
}

// emitAgentTOML converts a parsed Claude agent frontmatter + body into a
// codex agent TOML document. developer_instructions is the
// resolvedPathsPreamble rendered with skillsRoot, a blank line, then the
// body verbatim; model/sandbox/mcp_servers are intentionally omitted so
// the agent inherits them from the parent codex session.
//
// An EMPTY skillsRoot is an error naming the skills root. Rendering the
// preamble from an empty root would produce an agent whose first
// instruction points at nothing, which no caller can detect afterwards —
// so the install fails here instead of shipping it.
//
// Returns the marshaled TOML bytes. Deterministic: two calls on the
// same (fm, body, skillsRoot) produce byte-identical output.
func emitAgentTOML(fm frontmatter, body, skillsRoot string) ([]byte, error) {
	if skillsRoot == "" {
		return nil, fmt.Errorf("emitAgentTOML: skills root is empty; the resolved skills root is required to render the install-time resolved-paths preamble")
	}
	agent := codexAgent{
		Name:                  fm.Name,
		Description:           fm.Description,
		DeveloperInstructions: fmt.Sprintf(resolvedPathsPreamble, skillsRoot) + "\n\n" + body,
	}
	return toml.Marshal(agent)
}
